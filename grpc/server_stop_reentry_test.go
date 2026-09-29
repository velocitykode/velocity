package grpc_test

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
)

// stopCalls are the three ways to stop a server, each returning Shutdown's
// error (nil for the others).
var stopCalls = map[string]func(*grpc.Server) error{
	"Stop":         func(s *grpc.Server) error { s.Stop(); return nil },
	"GracefulStop": func(s *grpc.Server) error { s.GracefulStop(); return nil },
	"Shutdown":     func(s *grpc.Server) error { return s.Shutdown(context.Background()) },
}

// nestedStop runs a stop call once, from inside some user code the server
// calls, and records what it returned.
type nestedStop struct {
	server *atomic.Pointer[grpc.Server]
	stop   func(*grpc.Server) error
	armed  atomic.Bool
	fired  atomic.Bool
	done   chan struct{}
	err    error
}

func newNestedStop(stop func(*grpc.Server) error) *nestedStop {
	return &nestedStop{server: &atomic.Pointer[grpc.Server]{}, stop: stop, done: make(chan struct{})}
}

func (n *nestedStop) fire() {
	if !n.armed.Load() || !n.fired.CompareAndSwap(false, true) {
		return
	}
	defer close(n.done)
	n.err = n.stop(n.server.Load())
}

// stopLineLogger fires its nested stop from the server's stop line.
type stopLineLogger struct{ nested *nestedStop }

func (l *stopLineLogger) line(msg string) {
	if strings.Contains(msg, "stopping") {
		l.nested.fire()
	}
}
func (l *stopLineLogger) Debug(msg string, _ ...any)  { l.line(msg) }
func (l *stopLineLogger) Info(msg string, _ ...any)   { l.line(msg) }
func (l *stopLineLogger) Warn(msg string, _ ...any)   { l.line(msg) }
func (l *stopLineLogger) Error(msg string, _ ...any)  { l.line(msg) }
func (l *stopLineLogger) Fatal(msg string, _ ...any)  { l.line(msg) }
func (l *stopLineLogger) With(...any) contract.Logger { return l }

// nestedListener fires its nested stop from Close, which grpc-go calls
// while it stops the server, and from Addr once armed.
type nestedListener struct {
	net.Listener
	nested *nestedStop
	closed atomic.Bool
}

func (l *nestedListener) Close() error {
	l.nested.fire()
	l.closed.Store(true)
	return l.Listener.Close()
}

// checkNested waits for the outer stop and the nested one, and checks
// the nested call's result: a nested Shutdown reports it could not wait.
func checkNested(t *testing.T, s *grpc.Server, outer func(*grpc.Server) error, nested *nestedStop, nestedName string) {
	t.Helper()
	if p := hostile.Within(t, 3*time.Second, func() { _ = outer(s) }); p != nil {
		t.Fatalf("%s panicked: %v", "the outer stop", p)
	}
	select {
	case <-nested.done:
	case <-time.After(3 * time.Second):
		t.Fatal("the nested stop did not return")
	}
	if !nested.fired.Load() {
		t.Fatal("the nested stop never ran")
	}
	if nestedName == "Shutdown" {
		if !errors.Is(nested.err, grpcgo.ErrServerStopped) {
			t.Errorf("nested Shutdown = %v, want an error wrapping ErrServerStopped", nested.err)
		}
	} else if nested.err != nil {
		t.Errorf("nested %s = %v", nestedName, nested.err)
	}
}

// A stop called from the owner stop's own diagnostic never waits on that
// stop: every outer and nested pair returns, with no call in flight and
// with one in flight, the server stops, and ServerStopped comes once.
func TestServerStop_NestedStopFromTheStopLineDoesNotWaitOnItself(t *testing.T) {
	for outerName, outer := range stopCalls {
		for nestedName, nestedCall := range stopCalls {
			for _, inFlight := range []bool{false, true} {
				name := outerName + "/" + nestedName
				if inFlight {
					name += "/in_flight"
				}
				t.Run(name, func(t *testing.T) {
					nested := newNestedStop(nestedCall)
					lis := &closeTracker{Listener: loopback(t)}
					s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&stopLineLogger{nested: nested}))
					nested.server.Store(s)
					counter := &stoppedCounter{}
					s.SetEventDispatcher(counter.dispatch)
					var handlerDone atomic.Bool
					entered, release := make(chan struct{}), make(chan struct{})
					if inFlight {
						var once sync.Once
						s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
							once.Do(func() { close(entered) })
							defer handlerDone.Store(true)
							select {
							case <-release:
							case <-ctx.Done():
							}
							return h(ctx, req)
						})
					}
					client := startHealth(t, s)
					if inFlight {
						go func() {
							_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
						}()
						<-entered
						time.AfterFunc(100*time.Millisecond, func() { close(release) })
					}
					nested.armed.Store(true)

					var outerErr error
					checkNested(t, s, func(s *grpc.Server) error { outerErr = outer(s); return outerErr }, nested, nestedName)
					if outerName == "Shutdown" {
						if outerErr != nil {
							t.Errorf("outer Shutdown = %v, want nil", outerErr)
						}
						if inFlight && !handlerDone.Load() {
							t.Error("outer Shutdown returned nil before the call in flight ended")
						}
					}
					waitFor(t, "the listener to close", lis.closed.Load)
					waitFor(t, "ServerStopped", func() bool { return counter.n.Load() >= 1 })
					if s.IsRunning() {
						t.Error("server still running")
					}
					time.Sleep(20 * time.Millisecond)
					if got := counter.n.Load(); got != 1 {
						t.Errorf("ServerStopped dispatched %d times, want 1", got)
					}
				})
			}
		}
	}
}

// A stop called from the caller's listener while grpc-go closes it (under
// grpc-go's own lock) neither waits on the stop closing it nor re-enters
// grpc-go on that goroutine.
func TestServerStop_NestedStopFromTheListenersCloseDoesNotWaitOnItself(t *testing.T) {
	for outerName, outer := range stopCalls {
		for nestedName, nestedCall := range stopCalls {
			t.Run(outerName+"/"+nestedName, func(t *testing.T) {
				nested := newNestedStop(nestedCall)
				lis := &nestedListener{Listener: loopback(t), nested: nested}
				s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
				nested.server.Store(s)
				counter := &stoppedCounter{}
				s.SetEventDispatcher(counter.dispatch)
				startHealth(t, s)
				nested.armed.Store(true)

				checkNested(t, s, outer, nested, nestedName)
				waitFor(t, "ServerStopped", func() bool { return counter.n.Load() >= 1 })
				time.Sleep(20 * time.Millisecond)
				if got := counter.n.Load(); got != 1 {
					t.Errorf("ServerStopped dispatched %d times, want 1", got)
				}
			})
		}
	}
}

// waitFor fails the test when cond does not hold within a second.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", what)
}
