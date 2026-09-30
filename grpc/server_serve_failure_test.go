package grpc_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// A serve loop that fails (its supplied listener is already closed)
// stops the server: it no longer reports running, ServerStopped is
// dispatched once, a later start returns grpc.ErrServerStopped and a
// Shutdown returns nil.
func TestServer_FailedServeStopsTheServer(t *testing.T) {
	for _, start := range []string{"StartAsync", "Start"} {
		t.Run(start, func(t *testing.T) {
			lis := testnet.Loopback(t)
			_ = lis.Close()
			s := grpc.NewServer(grpc.WithListener(lis))
			var stopped atomic.Int32
			s.SetEventDispatcher(func(_ context.Context, ev any) error {
				if _, ok := ev.(*grpcevents.ServerStopped); ok {
					stopped.Add(1)
				}
				return nil
			})
			s.RegisterService(func(srv any) {
				grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), health.NewServer())
			})
			stopOnCleanup(t, s)
			switch start {
			case "StartAsync":
				if err := s.StartAsync(); err != nil {
					t.Fatalf("StartAsync = %v, want nil (the serve loop fails later)", err)
				}
			case "Start":
				var err error
				if p := hostile.Within(t, 2*time.Second, func() { err = s.Start() }); p != nil {
					t.Fatalf("%s panicked: %v", "Start", p)
				}
				if err == nil {
					t.Fatal("Start on a closed listener = nil, want the serve error")
				}
			}
			waitFor(t, "the server to stop", func() bool { return !s.IsRunning() })
			waitFor(t, "ServerStopped", func() bool { return stopped.Load() == 1 })
			if err := s.StartAsync(); !errors.Is(err, grpcgo.ErrServerStopped) {
				t.Errorf("StartAsync after the failed serve = %v, want grpc.ErrServerStopped", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				t.Errorf("Shutdown = %v, want nil", err)
			}
			if n := stopped.Load(); n != 1 {
				t.Errorf("ServerStopped dispatched %d times, want 1", n)
			}
		})
	}
}
