package grpc_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
)

// gatewayLogger calls back into the gateway from every line it writes.
type gatewayLogger struct {
	gateway *atomic.Pointer[grpc.Gateway]
	lines   atomic.Int32
}

func (l *gatewayLogger) touch() {
	l.lines.Add(1)
	if g := l.gateway.Load(); g != nil {
		_ = g.IsRunning()
		_ = g.GRPCEndpoint()
	}
}
func (l *gatewayLogger) Debug(string, ...any)        { l.touch() }
func (l *gatewayLogger) Info(string, ...any)         { l.touch() }
func (l *gatewayLogger) Warn(string, ...any)         { l.touch() }
func (l *gatewayLogger) Error(string, ...any)        { l.touch() }
func (l *gatewayLogger) Fatal(string, ...any)        { l.touch() }
func (l *gatewayLogger) With(...any) contract.Logger { return l }

// newCallbackGateway returns a gateway on an ephemeral port whose logger
// calls back into it.
func newCallbackGateway() (*grpc.Gateway, *gatewayLogger, *atomic.Pointer[grpc.Gateway]) {
	ref := &atomic.Pointer[grpc.Gateway]{}
	logger := &gatewayLogger{gateway: ref}
	g := grpc.NewGateway(grpc.GatewayWithPort("0"), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(logger))
	ref.Store(g)
	return g, logger, ref
}

// Gateway.Build runs application code (a registration handler, a
// middleware, the logger's warning) without holding the gateway's lock:
// each may call a gateway accessor, and Build still returns.
func TestGatewayBuild_UserCodeMayCallTheGateway(t *testing.T) {
	g, logger, ref := newCallbackGateway()
	var registered, wrapped atomic.Bool
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error {
		registered.Store(true)
		_ = ref.Load().Mux()
		_ = ref.Load().Address()
		return nil
	})
	g.Use(func(next http.Handler) http.Handler {
		wrapped.Store(true)
		_ = ref.Load().Port()
		return next
	})
	var err error
	within(t, 2*time.Second, "Gateway.Build", func() { err = g.Build(context.Background()) })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !registered.Load() || !wrapped.Load() {
		t.Errorf("registered %v, wrapped %v: want both", registered.Load(), wrapped.Load())
	}
	if g.Mux() == nil || logger.lines.Load() != 1 {
		t.Errorf("mux %v, warnings %d: want a built gateway and the insecure warning", g.Mux(), logger.lines.Load())
	}
}

// A Build called from inside a Build in progress returns
// ErrBuildInProgress at once, and the outer Build completes, running the
// registration once.
func TestGatewayBuild_ReentrantBuildReturnsAnError(t *testing.T) {
	g, _, ref := newCallbackGateway()
	var inner error
	var registrations atomic.Int32
	g.RegisterHandler(func(ctx context.Context, _ *runtime.ServeMux, _ string, _ []grpcgo.DialOption) error {
		registrations.Add(1)
		inner = ref.Load().Build(ctx)
		return nil
	})
	var err error
	within(t, 2*time.Second, "Gateway.Build", func() { err = g.Build(context.Background()) })
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !errors.Is(inner, grpc.ErrBuildInProgress) {
		t.Errorf("inner Build = %v, want ErrBuildInProgress", inner)
	}
	if registrations.Load() != 1 {
		t.Errorf("registrations ran %d times, want 1", registrations.Load())
	}
}

// A Build whose registration fails publishes nothing: the gateway is not
// left half-built, and a later Build runs the registrations again.
func TestGatewayBuild_FailedRegistrationPublishesNothing(t *testing.T) {
	g := quietGateway()
	var fail atomic.Bool
	fail.Store(true)
	g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error {
		if fail.Load() {
			return errors.New("upstream not ready")
		}
		return nil
	})
	if err := g.Build(context.Background()); err == nil {
		t.Fatal("Build = nil, want the registration's error")
	}
	if g.Mux() != nil {
		t.Fatal("mux published after a failed Build")
	}
	fail.Store(false)
	if err := g.Build(context.Background()); err != nil || g.Mux() == nil {
		t.Fatalf("Build after the failure = %v, mux %v: want a built gateway", err, g.Mux())
	}
}

// quietGateway returns a gateway on an ephemeral port with a logger that
// does not call back.
func quietGateway() *grpc.Gateway {
	return grpc.NewGateway(grpc.GatewayWithPort("0"), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(&gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}}))
}

// Many concurrent Builds construct one gateway: each returns nil or
// ErrBuildInProgress, and the registration runs once. Under -race there
// is no data race.
func TestGatewayBuild_ConcurrentBuildsBuildOnce(t *testing.T) {
	for range 20 {
		g := quietGateway()
		var registrations atomic.Int32
		g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error {
			registrations.Add(1)
			return nil
		})
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := g.Build(context.Background()); err != nil && !errors.Is(err, grpc.ErrBuildInProgress) {
					t.Errorf("Build: %v", err)
				}
				_ = g.Mux()
			}()
		}
		wg.Wait()
		if registrations.Load() != 1 || g.Mux() == nil {
			t.Fatalf("registrations %d, mux %v: want one build", registrations.Load(), g.Mux())
		}
	}
}

// Gateway.Stop logs without holding the gateway's lock: a logger that
// calls a gateway accessor still lets Stop return.
func TestGatewayStop_LoggerMayCallTheGateway(t *testing.T) {
	g, _, _ := newCallbackGateway()
	if err := g.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	within(t, 2*time.Second, "Gateway.Stop", g.Stop)
	if g.IsRunning() {
		t.Error("gateway still running after Stop")
	}
}
