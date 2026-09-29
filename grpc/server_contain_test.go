package grpc_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/hostile"
)

// probeReports counts the reports a probe server makes, and the late ones.
type probeReports struct {
	mu          sync.Mutex
	total, late int
}

func (r *probeReports) Report(_ error, ec *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total++
	if ec.Extra["late"] == true {
		r.late++
	}
}

// runContainProbe serves one call through owner -> timeout (runs the rest
// of the chain on a goroutine of its own) -> panicking user interceptor ->
// tail -> handler, on a framework-built server, and fails t unless the
// panic was reported once (late when it came after the call ended).
func runContainProbe(t *testing.T, kind, when string) {
	reports := &probeReports{}
	s := grpc.NewServer(grpc.WithListener(loopback(t)), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}), grpc.WithReporter(reports))
	returned, finished := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finished) }) }
	waitReturned := func() {
		if when == "after" {
			<-returned
			time.Sleep(50 * time.Millisecond) // let the owner end the call
		}
	}
	s.Use(
		func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
			defer close(returned)
			go func() {
				defer finish()
				_, _ = h(ctx, req)
			}()
			if when == "before" {
				<-finished
			}
			return nil, status.Error(codes.DeadlineExceeded, "timed out")
		},
		func(context.Context, any, *grpcgo.UnaryServerInfo, grpcgo.UnaryHandler) (any, error) {
			waitReturned()
			panic("user interceptor broke")
		},
	)
	s.UseStream(
		func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
			defer close(returned)
			go func() {
				defer finish()
				_ = h(srv, ss)
			}()
			if when == "before" {
				<-finished
			}
			return status.Error(codes.DeadlineExceeded, "timed out")
		},
		func(any, grpcgo.ServerStream, *grpcgo.StreamServerInfo, grpcgo.StreamHandler) error {
			waitReturned()
			panic("user interceptor broke")
		},
	)
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
	client := grpc_health_v1.NewHealthClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if kind == "unary" {
		_, _ = client.Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	} else if w, err := client.Watch(ctx, &grpc_health_v1.HealthCheckRequest{}); err == nil {
		_, _ = w.Recv()
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("the continuation never finished")
	}
	time.Sleep(50 * time.Millisecond)
	reports.mu.Lock()
	total, late := reports.total, reports.late
	reports.mu.Unlock()
	wantLate := 0
	if when == "after" {
		wantLate = 1
	}
	if total != 1 || late != wantLate {
		t.Errorf("reports: %d, %d late; want 1, %d late", total, late, wantLate)
	}
}

// A user interceptor that panics on a goroutine an earlier interceptor
// started (a timeout, say) is contained on that goroutine by the server's
// default chain, unary and stream, whether the panic comes before or after
// the call ended: the process survives and the panic is reported once,
// late when the call had already ended. Each case runs isolated, since an
// uncontained panic there ends the process.
func TestServer_UserInterceptorPanicOnAnotherGoroutineIsContained(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		for _, when := range []string{"before", "after"} {
			t.Run(kind+"/panic "+when+" the call ended", func(t *testing.T) {
				hostile.Isolated(t, func() { runContainProbe(t, kind, when) })
			})
		}
	}
}
