package grpc_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A server a stop ended does not start again: Start and StartAsync return
// grpc.ErrServerStopped without marking it running or dispatching a
// second ServerStarted.
func TestServerStart_AfterAStopReturnsErrServerStopped(t *testing.T) {
	for name, start := range map[string]func(*grpc.Server) error{
		"Start":      (*grpc.Server).Start,
		"StartAsync": (*grpc.Server).StartAsync,
	} {
		t.Run(name, func(t *testing.T) {
			s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}))
			var started atomic.Int32
			s.SetEventDispatcher(func(_ context.Context, ev any) error {
				if _, ok := ev.(*grpcevents.ServerStarted); ok {
					started.Add(1)
				}
				return nil
			})
			client := startHealth(t, s)
			// StartAsync dispatches ServerStarted from its serve goroutine;
			// a call completing proves that dispatch returned.
			served(t, client)
			s.Stop()

			var err error
			if p := hostile.Within(t, 2*time.Second, func() { err = start(s) }); p != nil {
				t.Fatalf("%s panicked: %v", name, p)
			}
			if !errors.Is(err, grpcgo.ErrServerStopped) {
				t.Errorf("%s after Stop = %v, want ErrServerStopped", name, err)
			}
			if s.IsRunning() {
				t.Error("server marked running after a refused start")
			}
			if got := started.Load(); got != 1 {
				t.Errorf("ServerStarted dispatched %d times, want 1", got)
			}
		})
	}
}
