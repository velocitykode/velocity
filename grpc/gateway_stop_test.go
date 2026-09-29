package grpc_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
)

// slowGateway is a started gateway whose every request blocks until
// release is closed or the request's context ends.
type slowGateway struct {
	g       *grpc.Gateway
	addr    string
	entered chan struct{}
	release chan struct{}
	done    atomic.Bool
}

func startSlowGateway(t *testing.T, logger contract.Logger) *slowGateway {
	t.Helper()
	port := freePort(t)
	sg := &slowGateway{addr: "127.0.0.1:" + port, entered: make(chan struct{}, 8), release: make(chan struct{})}
	sg.g = grpc.NewGateway(grpc.GatewayWithPort(port), grpc.GatewayWithGRPCEndpoint("127.0.0.1:1"),
		grpc.GatewayWithEnvironment("development"), grpc.GatewayWithLogger(logger))
	sg.g.RegisterHandler(func(context.Context, *runtime.ServeMux, string, []grpcgo.DialOption) error { return nil })
	sg.g.Use(func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sg.entered <- struct{}{}
			select {
			case <-sg.release:
			case <-r.Context().Done():
			}
			sg.done.Store(true)
		})
	})
	if err := sg.g.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	waitFor(t, "the gateway to listen", func() bool {
		c, err := net.Dial("tcp", sg.addr)
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		sg.g.Stop()
		_ = sg.g.Shutdown(ctx)
	})
	return sg
}

// inFlight starts one request and waits until its handler runs.
func (sg *slowGateway) inFlight(t *testing.T) {
	t.Helper()
	go func() {
		resp, err := http.Get("http://" + sg.addr + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-sg.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached the handler")
	}
}

// A Gateway Shutdown that overlaps one already draining does not return
// nil before that drain has finished.
func TestGatewayShutdown_OverlappingShutdownWaitsForTheDrain(t *testing.T) {
	sg := startSlowGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}})
	sg.inFlight(t)

	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- sg.g.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	go func() { second <- sg.g.Shutdown(context.Background()) }()
	select {
	case err := <-second:
		t.Fatalf("second Shutdown returned %v while the first drain still had a request in flight", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(sg.release)
	for name, ch := range map[string]chan error{"first": first, "second": second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("%s Shutdown = %v, want nil", name, err)
			}
			if !sg.done.Load() {
				t.Errorf("%s Shutdown returned before the request ended", name)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s Shutdown did not return", name)
		}
	}
}

// An overlapping Gateway Shutdown whose own ctx ends first returns that
// ctx's error.
func TestGatewayShutdown_OverlappingShutdownReturnsItsDeadline(t *testing.T) {
	sg := startSlowGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}})
	sg.inFlight(t)
	first := make(chan error, 1)
	go func() { first <- sg.g.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var err error
	if p := hostile.Within(t, 2*time.Second, func() { err = sg.g.Shutdown(ctx) }); p != nil {
		t.Fatalf("%s panicked: %v", "second Shutdown", p)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second Shutdown = %v, want its deadline", err)
	}
	close(sg.release)
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first Shutdown did not return")
	}
}

// A Gateway Stop during a Shutdown closes the gateway: the request in
// flight is cut and the Shutdown returns.
func TestGatewayStop_DuringShutdownCloses(t *testing.T) {
	sg := startSlowGateway(t, &gatewayLogger{gateway: &atomic.Pointer[grpc.Gateway]{}})
	sg.inFlight(t)
	shut := make(chan error, 1)
	go func() { shut <- sg.g.Shutdown(context.Background()) }()
	time.Sleep(50 * time.Millisecond)
	if p := hostile.Within(t, 2*time.Second, sg.g.Stop); p != nil {
		t.Fatalf("%s panicked: %v", "Stop", p)
	}
	select {
	case <-shut:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after Stop closed the gateway")
	}
	waitFor(t, "the request to be cut", sg.done.Load)
}

// gatewayStopLineLogger calls a stop on the gateway from its stop line.
type gatewayStopLineLogger struct {
	gateway *atomic.Pointer[grpc.Gateway]
	stop    func(*grpc.Gateway) error
	fired   atomic.Bool
	err     error
	done    chan struct{}
}

func (l *gatewayStopLineLogger) line(msg string) {
	if (strings.Contains(msg, "stopping") || strings.Contains(msg, "shutting down")) && l.fired.CompareAndSwap(false, true) {
		defer close(l.done)
		l.err = l.stop(l.gateway.Load())
	}
}
func (l *gatewayStopLineLogger) Debug(msg string, _ ...any)  { l.line(msg) }
func (l *gatewayStopLineLogger) Info(msg string, _ ...any)   { l.line(msg) }
func (l *gatewayStopLineLogger) Warn(msg string, _ ...any)   { l.line(msg) }
func (l *gatewayStopLineLogger) Error(msg string, _ ...any)  { l.line(msg) }
func (l *gatewayStopLineLogger) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l *gatewayStopLineLogger) With(...any) contract.Logger { return l }

// A Gateway stop called from the gateway's own stop line never waits on
// that stop, with no request in flight and with one in flight; a nested
// Shutdown reports that it could not wait.
func TestGatewayStop_NestedStopFromTheStopLineDoesNotWaitOnItself(t *testing.T) {
	calls := map[string]func(*grpc.Gateway) error{
		"Stop":     func(g *grpc.Gateway) error { g.Stop(); return nil },
		"Shutdown": func(g *grpc.Gateway) error { return g.Shutdown(context.Background()) },
	}
	for outerName, outer := range calls {
		for nestedName, nested := range calls {
			for _, inFlight := range []bool{false, true} {
				name := outerName + "/" + nestedName
				if inFlight {
					name += "/in_flight"
				}
				t.Run(name, func(t *testing.T) {
					logger := &gatewayStopLineLogger{gateway: &atomic.Pointer[grpc.Gateway]{}, stop: nested, done: make(chan struct{})}
					sg := startSlowGateway(t, logger)
					logger.gateway.Store(sg.g)
					if inFlight {
						sg.inFlight(t)
						time.AfterFunc(100*time.Millisecond, func() { close(sg.release) })
					}
					var outerErr error
					if p := hostile.Within(t, 3*time.Second, func() { outerErr = outer(sg.g) }); p != nil {
						t.Fatalf("%s panicked: %v", "the outer stop", p)
					}
					select {
					case <-logger.done:
					case <-time.After(3 * time.Second):
						t.Fatal("the nested stop did not return")
					}
					if nestedName == "Shutdown" {
						if !errors.Is(logger.err, http.ErrServerClosed) || !errors.Is(logger.err, contract.ErrStopFromOwnWork) {
							t.Errorf("nested Shutdown = %v, want an error wrapping http.ErrServerClosed and contract.ErrStopFromOwnWork", logger.err)
						}
					}
					if outerName == "Shutdown" && outerErr != nil {
						t.Errorf("outer Shutdown = %v, want nil", outerErr)
					}
					waitFor(t, "the gateway to stop accepting", func() bool {
						c, err := net.Dial("tcp", sg.addr)
						if err == nil {
							_ = c.Close()
						}
						return err != nil
					})
				})
			}
		}
	}
}
