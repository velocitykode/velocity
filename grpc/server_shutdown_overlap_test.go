package grpc_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
)

// A Shutdown that overlaps one already draining does not report success
// before that drain has finished: a caller that tears its dependencies
// down after a nil Shutdown must not pull them from under a handler.
func TestServerShutdown_OverlappingShutdownWaitsForTheDrain(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var handlerDone atomic.Bool
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		close(entered)
		<-release
		defer handlerDone.Store(true)
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- s.Shutdown(ctx) }()
	// Either Shutdown may own the drain; the other overlaps it. A pause,
	// not an IsRunning poll, so the test also runs where a stop held the
	// server's lock across the drain.
	time.Sleep(50 * time.Millisecond)

	second := make(chan error, 1)
	go func() { second <- s.Shutdown(ctx) }()
	select {
	case err := <-second:
		t.Fatalf("second Shutdown returned %v while the first drain still had a call in flight", err)
	case <-time.After(150 * time.Millisecond):
	}

	close(release)
	for name, ch := range map[string]chan error{"first": first, "second": second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Errorf("%s Shutdown = %v, want nil", name, err)
			}
			if !handlerDone.Load() {
				t.Errorf("%s Shutdown returned before the handler ended", name)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s Shutdown did not return after the drain", name)
		}
	}
}

// An overlapping Shutdown whose own ctx ends first returns that ctx's
// error, not nil.
func TestServerShutdown_OverlappingShutdownReturnsItsDeadline(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		close(entered)
		<-release // ignores ctx
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered

	first := make(chan error, 1)
	go func() { first <- s.Shutdown(context.Background()) }()
	for s.IsRunning() {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var err error
	within(t, 2*time.Second, "second Shutdown", func() { err = s.Shutdown(ctx) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second Shutdown = %v, want its deadline", err)
	}
	close(release)
	select {
	case <-first:
	case <-time.After(3 * time.Second):
		t.Fatal("first Shutdown did not return")
	}
}
