package grpc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// newTestGateway returns a gateway serving on a loopback listener of the
// test's own, with reg as its one registration, and that listener.
func newTestGateway(t *testing.T, logger contract.Logger, reg grpc.GatewayRegistrationFunc) (*grpc.Gateway, *acceptTracker) {
	t.Helper()
	lis := &acceptTracker{Listener: testnet.Loopback(t)}
	g := grpc.NewGateway(grpc.GatewayWithListener(lis), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(logger))
	if reg == nil {
		reg = func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil }
	}
	g.RegisterHandler(reg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		g.Stop()
		_ = g.Shutdown(ctx)
	})
	return g, lis
}

// acceptTracker records whether a serve loop called Accept on the
// listener: whether the gateway took connections from it. The listener is
// the test's, so the answer does not depend on who else holds a port.
type acceptTracker struct {
	net.Listener
	accepted atomic.Bool
}

func (l *acceptTracker) Accept() (net.Conn, error) {
	l.accepted.Store(true)
	return l.Listener.Accept()
}

// A stop during the Build a start runs is not lost: the Build publishes
// nothing, the start returns http.ErrServerClosed instead of serving, and
// the gateway never takes a connection from its listener. The stop comes from outside
// (Stop, which does not wait on the Build) or from the Build's own
// registration (Shutdown, refused there but begun).
func TestGatewayStart_StopDuringItsBuildIsNotLost(t *testing.T) {
	for _, stopper := range []string{"outside Stop", "registration Shutdown"} {
		for name, start := range map[string]func(*grpc.Gateway) error{
			"Start":      (*grpc.Gateway).Start,
			"StartAsync": (*grpc.Gateway).StartAsync,
		} {
			t.Run(stopper+"/"+name, func(t *testing.T) {
				entered, release := make(chan struct{}), make(chan struct{})
				var ref atomic.Pointer[grpc.Gateway]
				var nestedErr error
				g, lis := newTestGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}},
					func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error {
						if stopper == "registration Shutdown" {
							nestedErr = ref.Load().Shutdown(context.Background())
							return nil
						}
						close(entered)
						<-release
						return nil
					})
				ref.Store(g)
				started := make(chan error, 1)
				go func() { started <- start(g) }()
				if stopper == "outside Stop" {
					select {
					case <-entered:
					case <-time.After(hostile.Deadline):
						t.Fatal("the Build never ran its registration")
					}
					if p := hostile.Within(t, hostile.Deadline, g.Stop); p != nil {
						t.Fatalf("Stop panicked: %v", p)
					}
					close(release)
				}
				select {
				case err := <-started:
					if !errors.Is(err, http.ErrServerClosed) {
						t.Errorf("%s = %v, want http.ErrServerClosed", name, err)
					}
				case <-time.After(hostile.Deadline):
					t.Fatalf("%s served past the stop", name)
				}
				if stopper == "registration Shutdown" && !errors.Is(nestedErr, contract.ErrStopFromOwnWork) {
					t.Errorf("Shutdown from the registration = %v, want contract.ErrStopFromOwnWork", nestedErr)
				}
				if lis.accepted.Load() || g.IsRunning() {
					t.Error("the gateway serves after a stop during its Build")
				}
			})
		}
	}
}

// Stop closes the gateway without waiting for the handlers, but the
// gateway is not reported stopped while a request admitted before the
// stop still runs: a Shutdown returns nil only once its handler has
// returned.
func TestGatewayStop_StoppedOnlyOnceTheAdmittedRequestsReturn(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var done atomic.Bool
	g, lis := newTestGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}}, nil)
	g.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(entered)
			<-release // ignores the request's context
			done.Store(true)
		})
	})
	if err := g.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	go func() {
		if resp, err := http.Get("http://" + lis.Addr().String() + "/"); err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(hostile.Deadline):
		t.Fatal("the request never reached the handler")
	}
	if p := hostile.Within(t, hostile.Deadline, g.Stop); p != nil {
		t.Fatalf("Stop panicked: %v", p)
	}
	shut := make(chan error, 1)
	go func() { shut <- g.Shutdown(context.Background()) }()
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned %v while an admitted request still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-shut:
		if err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
		if !done.Load() {
			t.Error("Shutdown returned before the request's handler did")
		}
	case <-time.After(hostile.Deadline):
		t.Fatal("Shutdown did not return after the request ended")
	}
}

// StartAsync binds the gateway's port before it returns: a port already
// taken is its error, not a line logged later from a goroutine.
func TestGatewayStartAsync_ReturnsTheBindError(t *testing.T) {
	// The gateway binds all interfaces, so the port is taken there too.
	taken, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = taken.Close() }()
	_, port, _ := net.SplitHostPort(taken.Addr().String())
	g := grpc.NewGateway(grpc.GatewayWithPort(port), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(&gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}}))
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	t.Cleanup(g.Stop)
	if p := hostile.Within(t, hostile.Deadline, func() { err = g.StartAsync() }); p != nil {
		t.Fatalf("StartAsync panicked: %v", p)
	}
	if err == nil {
		t.Fatal("StartAsync on a taken port = nil, want the bind error")
	}
	if g.IsRunning() {
		t.Error("gateway reports running after a failed bind")
	}
}

// StartAsync publishes the start before it returns: the gateway reports
// running (set when its serve loop enters the first Accept) and the
// starting line is written; a request then gets the gateway's answer.
func TestGatewayStartAsync_PublishesTheStartBeforeReturning(t *testing.T) {
	for range 50 {
		log := &startLog{}
		g, lis := newTestGateway(t, gatewayStartLog{log}, nil)
		if err := g.StartAsync(); err != nil {
			t.Fatalf("StartAsync: %v", err)
		}
		running, lines := g.IsRunning(), log.all()
		resp, err := http.Get("http://" + lis.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
		g.Stop()
		if !running || len(lines) != 1 {
			t.Fatalf("at StartAsync's return: running %v, lines %v: want both published", running, lines)
		}
		if err != nil {
			t.Fatalf("request after StartAsync: %v", err)
		}
	}
}

// gatewayStartLog records the gateway's starting line in a startLog.
type gatewayStartLog struct{ log *startLog }

func (l gatewayStartLog) line(msg string) {
	if msg == "HTTP gateway starting" {
		l.log.add("line")
	}
}
func (l gatewayStartLog) Debug(msg string, _ ...any)  { l.line(msg) }
func (l gatewayStartLog) Info(msg string, _ ...any)   { l.line(msg) }
func (l gatewayStartLog) Warn(msg string, _ ...any)   { l.line(msg) }
func (l gatewayStartLog) Error(msg string, _ ...any)  { l.line(msg) }
func (l gatewayStartLog) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l gatewayStartLog) With(...any) contract.Logger { return l }
