package grpc_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"

	"github.com/velocitykode/velocity/grpc"
)

// panickyContextStream is a user stream wrapper whose Context panics.
type panickyContextStream struct{ grpcgo.ServerStream }

func (panickyContextStream) Context() context.Context { panic("stream context broke") }

// runStreamGetterProbe serves one stream call through owner -> a timeout
// interceptor that runs the rest of the chain on a goroutine of its own
// with a stream whose Context panics -> (optionally) a pass-through user
// interceptor -> tail -> handler, and exits probeContained when the
// process survived and the call was reported once.
func runStreamGetterProbe(through bool) {
	reports := &probeReports{}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	s := grpc.NewServer(grpc.WithListener(lis), grpc.WithLogger(&reentrantLogger{server: &atomic.Pointer[grpc.Server]{}}), grpc.WithReporter(reports))
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
		os.Exit(3)
	}
	conn, err := grpcgo.NewClient(s.Address(), grpcgo.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		os.Exit(3)
	}
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
		os.Exit(probeBadReports)
	}
	os.Exit(probeContained)
}

// A stream whose Context panics, handed down on a goroutine a timeout
// interceptor started, is contained: the chain's containment installs its
// recovery before it reads the stream, and never reads the failing getter
// again, so the process survives and the call is reported once. It runs in
// a child process, since an uncontained panic there ends the process.
func TestServer_PanickingStreamContextIsContained(t *testing.T) {
	if mode := os.Getenv("VELOCITY_GRPC_STREAM_GETTER_PROBE"); mode != "" {
		runStreamGetterProbe(mode == "through")
		return
	}
	for _, mode := range []string{"direct", "through"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestServer_PanickingStreamContextIsContained$")
			cmd.Env = append(os.Environ(), "VELOCITY_GRPC_STREAM_GETTER_PROBE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				code := -1
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				}
				if code == probeBadReports {
					t.Fatalf("contained, but not reported once")
				}
				t.Fatalf("probe process died (exit %d): the stream getter panic was not contained\n%s", code, tail(out))
			}
		})
	}
}
