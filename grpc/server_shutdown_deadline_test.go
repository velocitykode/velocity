package grpc_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// hangingCall starts a call whose handler ignores its context until
// release is closed, and waits until it runs.
func hangingCall(t *testing.T, s *grpc.Server) (release chan struct{}) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.Use(func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
		once.Do(func() { close(entered) })
		<-release
		return h(ctx, req)
	})
	client := startHealth(t, s)
	go func() {
		_, _ = client.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	}()
	<-entered
	return release
}

// shutdownOnTime runs Shutdown with a short deadline and fails the test
// unless it returns the deadline about on time.
func shutdownOnTime(t *testing.T, shutdown func(context.Context) error) {
	t.Helper()
	const deadline = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	var err error
	if p := hostile.Within(t, 2*time.Second, func() { err = shutdown(ctx) }); p != nil {
		t.Fatalf("%s panicked: %v", "Shutdown", p)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want its deadline", err)
	}
}

// A ServerStopped listener that calls GracefulStop does not hold a
// timed-out Shutdown past its deadline: the event comes once the drain
// has ended.
func TestServerShutdown_StoppedListenerDoesNotHoldTheDeadline(t *testing.T) {
	s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
	var stopped atomic.Int32
	s.SetEventDispatcher(func(_ context.Context, ev any) error {
		if _, ok := ev.(*grpcevents.ServerStopped); ok {
			stopped.Add(1)
			s.GracefulStop()
		}
		return nil
	})
	release := hangingCall(t, s)
	shutdownOnTime(t, s.Shutdown)
	close(release)
	waitFor(t, "ServerStopped", func() bool { return stopped.Load() == 1 })
}

// A stop line or ServerStopped dispatch that blocks does not hold
// Shutdown past its deadline.
func TestServerShutdown_BlockingUserCodeDoesNotHoldTheDeadline(t *testing.T) {
	t.Run("logger", func(t *testing.T) {
		logger := newPausingLogger("gracefully stopping")
		s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(logger))
		startHealth(t, s)
		defer close(logger.release)
		shutdownOnTime(t, s.Shutdown)
	})
	t.Run("dispatcher", func(t *testing.T) {
		s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
		block := make(chan struct{})
		defer close(block)
		s.SetEventDispatcher(func(_ context.Context, ev any) error {
			if _, ok := ev.(*grpcevents.ServerStopped); ok {
				<-block
			}
			return nil
		})
		startHealth(t, s)
		shutdownOnTime(t, s.Shutdown)
	})
}

// A gateway stop line that blocks does not hold Shutdown past its
// deadline.
func TestGatewayShutdown_BlockingLoggerDoesNotHoldTheDeadline(t *testing.T) {
	logger := newPausingLogger("shutting down")
	sg := startSlowGateway(t, logger)
	defer close(logger.release)
	shutdownOnTime(t, sg.g.Shutdown)
}
