package grpc_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A handler that calls GracefulStop gets it back at once: GracefulStop
// starts the drain and does not wait for it, so it does not wait for the
// handler calling it.
func TestServerGracefulStop_FromAHandlerReturns(t *testing.T) {
	var ref atomic.Pointer[grpc.Server]
	returned := make(chan struct{})
	var once sync.Once
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	ref.Store(s)
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		once.Do(func() {
			ref.Load().GracefulStop()
			close(returned)
		})
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		t.Fatal("GracefulStop from a handler waited for that handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown after the handler = %v, want nil", err)
	}
}

// An outside GracefulStop returns before a slow handler finishes, and a
// Shutdown after it waits for that drain.
func TestServerGracefulStop_ReturnsAtOnceAndShutdownWaits(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var handlerDone atomic.Bool
	var once sync.Once
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		once.Do(func() { close(entered) })
		<-release
		defer handlerDone.Store(true)
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered

	if p := hostile.Within(t, time.Second, s.GracefulStop); p != nil {
		t.Fatalf("%s panicked: %v", "GracefulStop", p)
	}
	if handlerDone.Load() {
		t.Fatal("the handler finished before the test released it")
	}
	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shut <- s.Shutdown(ctx)
	}()
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned %v before the drain GracefulStop started had ended", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-shut:
		if err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
		if !handlerDone.Load() {
			t.Error("Shutdown returned before the handler ended")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after the drain ended")
	}
}
