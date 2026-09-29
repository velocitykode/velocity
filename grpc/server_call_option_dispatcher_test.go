package grpc_test

import (
	"context"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// callEventCounter counts a call's started and completed events.
type callEventCounter struct{ started, completed atomic.Int32 }

func (c *callEventCounter) dispatch(_ context.Context, ev any) error {
	switch ev.(type) {
	case *grpcevents.RequestStarted:
		c.started.Add(1)
	case *grpcevents.RequestCompleted:
		c.completed.Add(1)
	}
	return nil
}

// A raw CallOption that sets CallConfig.EventDispatcher overrides the
// server's dispatcher on the default chain, as WithCallOptions documents:
// the call's events go to it and not to the server's.
func TestServer_RawCallOptionDispatcherWins(t *testing.T) {
	custom, server := &callEventCounter{}, &callEventCounter{}
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}),
		grpc.WithCallOptions(func(c *interceptors.CallConfig) {
			c.SkipHealthChecks = false
			c.EventDispatcher = custom.dispatch
		}))
	s.SetEventDispatcher(server.dispatch)
	client := startHealth(t, s)
	if _, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if custom.started.Load() != 1 || custom.completed.Load() != 1 {
		t.Errorf("custom dispatcher got started %d completed %d, want 1 1", custom.started.Load(), custom.completed.Load())
	}
	if server.started.Load() != 0 || server.completed.Load() != 0 {
		t.Errorf("server dispatcher got started %d completed %d, want none", server.started.Load(), server.completed.Load())
	}
}

// WithCallOptions(WithEventDispatcher(nil)) turns the call's events off on
// the default chain even while the server has a dispatcher.
func TestServer_WithEventDispatcherNilTurnsCallEventsOff(t *testing.T) {
	server := &callEventCounter{}
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}),
		grpc.WithCallOptions(
			func(c *interceptors.CallConfig) { c.SkipHealthChecks = false },
			interceptors.WithEventDispatcher(nil),
		))
	s.SetEventDispatcher(server.dispatch)
	client := startHealth(t, s)
	if _, err := client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{}); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if server.started.Load() != 0 || server.completed.Load() != 0 {
		t.Errorf("server dispatcher got started %d completed %d, want none", server.started.Load(), server.completed.Load())
	}
}
