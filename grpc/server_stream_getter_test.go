package grpc_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/testnet"
)

// panickyContextStream is a user stream wrapper whose Context panics.
type panickyContextStream struct{ grpcgo.ServerStream }

func (panickyContextStream) Context() context.Context { panic("stream context broke") }

// runStreamGetterProbe serves one stream call through owner -> a timeout
// interceptor that runs the rest of the chain on a goroutine of its own
// with a stream whose Context panics -> (optionally) a pass-through user
// interceptor -> tail -> handler, and fails t unless the call was
// reported once.
func runStreamGetterProbe(t *testing.T, through bool) {
	reports := &probeReports{}
	s := grpc.NewServer(grpc.WithListener(testnet.Loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}), grpc.WithReporter(reports))
	interceptors := []grpcgo.StreamServerInterceptor{
		func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
			done := make(chan error, 1)
			go func() { done <- h(srv, panickyContextStream{ss}) }()
			return <-done
		},
	}
	if through {
		interceptors = append(interceptors, func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
			return h(srv, ss)
		})
	}
	s.UseStream(interceptors...)
	s.RegisterService(func(srv any) {
		grpc_health_v1.RegisterHealthServer(srv.(*grpcgo.Server), health.NewServer())
	})
	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}
	stopOnCleanup(t, s)
	conn, err := grpcgo.NewClient(s.Address(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if w, err := grpc_health_v1.NewHealthClient(conn).Watch(ctx, &grpc_health_v1.HealthCheckRequest{}); err == nil {
		_, _ = w.Recv()
	}
	time.Sleep(100 * time.Millisecond)
	reports.mu.Lock()
	total := reports.total
	reports.mu.Unlock()
	if total != 1 {
		t.Errorf("reports = %d, want 1", total)
	}
}

// A stream whose Context panics, handed down on a goroutine a timeout
// interceptor started, is contained: the chain's containment installs its
// recovery before it reads the stream, and never reads the failing getter
// again, so the process survives and the call is reported once. Each case
// runs isolated, since an uncontained panic there ends the process.
func TestServer_PanickingStreamContextIsContained(t *testing.T) {
	for _, mode := range []string{"direct", "through"} {
		t.Run(mode, func(t *testing.T) {
			hostile.Isolated(t, func() { runStreamGetterProbe(t, mode == "through") })
		})
	}
}
