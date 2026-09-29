package grpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/log"
)

// defaultUnaryCall returns one successful unary call through the default
// chain of s, composed once.
func defaultUnaryCall(s *Server) func() {
	calls := s.defaultCallLifecycle(s.logger, s.reporter, nil)
	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Events/Do"}
	final := func(context.Context, any) (any, error) { return nil, nil }
	inner := func(ctx context.Context, req any) (any, error) { return calls.Unary(ctx, req, info, final) }
	return func() { _, _ = calls.Unary(context.Background(), nil, info, inner) }
}

func quietServer() *Server {
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	return NewServer(WithLogger(quiet))
}

// The default call lifecycle reads the Server's live dispatcher: one set
// after the chain was built receives the call's events, and once it is
// cleared no event is built at all (the call costs what it costs on a
// server that never had one).
func TestDefaultCallLifecycle_FollowsTheServersDispatcher(t *testing.T) {
	s := quietServer()
	call := defaultUnaryCall(s)
	never := testing.AllocsPerRun(200, call)

	var started, completed atomic.Int32
	s.SetEventDispatcher(func(_ context.Context, ev any) error {
		switch ev.(type) {
		case *grpcevents.RequestStarted:
			started.Add(1)
		case *grpcevents.RequestCompleted:
			completed.Add(1)
		}
		return nil
	})
	call()
	if started.Load() != 1 || completed.Load() != 1 {
		t.Fatalf("started %d completed %d, want 1 1 from a dispatcher set after the chain was built", started.Load(), completed.Load())
	}
	installed := testing.AllocsPerRun(200, call)

	s.SetEventDispatcher(nil)
	cleared := testing.AllocsPerRun(200, call)
	if cleared >= installed || cleared != never {
		t.Errorf("allocs: never %v, installed %v, cleared %v: want cleared equal to never and below installed", never, installed, cleared)
	}
}

// A dispatcher that fails a call's event, by an error or a panic, meets
// the Server's one failure policy: each failed event is counted once.
func TestDefaultCallLifecycle_FailedDispatchIsCountedOnceByTheServer(t *testing.T) {
	for name, dispatch := range map[string]func(context.Context, any) error{
		"error": func(context.Context, any) error { return errors.New("sink down") },
		"panic": func(context.Context, any) error { panic("sink broke") },
	} {
		t.Run(name, func(t *testing.T) {
			s := quietServer()
			s.SetEventDispatcher(dispatch)
			defaultUnaryCall(s)()
			// RequestStarted and RequestCompleted each failed once.
			if got := s.events.FailureCount(); got != 2 {
				t.Errorf("server failure count = %d, want 2 (one per failed event)", got)
			}
		})
	}
}

// A dispatcher passed through WithCallOptions still wins over the
// Server's: the call events reach it, not the Server's dispatcher.
func TestDefaultCallLifecycle_CallOptionDispatcherWins(t *testing.T) {
	var server, option atomic.Int32
	quiet, _ := log.NewLogger(log.LogConfig{Driver: "null"})
	s := NewServer(WithLogger(quiet), WithCallOptions(interceptors.WithEventDispatcher(func(context.Context, any) error {
		option.Add(1)
		return nil
	})))
	s.SetEventDispatcher(func(context.Context, any) error { server.Add(1); return nil })
	calls := s.defaultCallLifecycle(s.logger, s.reporter, s.callOptions)
	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Events/Do"}
	final := func(context.Context, any) (any, error) { return nil, nil }
	_, _ = calls.Unary(context.Background(), nil, info, final)
	if option.Load() != 2 || server.Load() != 0 {
		t.Errorf("option dispatcher got %d, server's got %d: want 2 and 0", option.Load(), server.Load())
	}
}
