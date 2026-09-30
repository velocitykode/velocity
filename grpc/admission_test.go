package grpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/drain"
)

// A call admitted before the stop began holds the run until its handler
// returns; a call that arrives after is refused with codes.Unavailable
// and never reaches its handler. Unary and stream alike.
func TestAdmission_HoldsTheRunAndRefusesOnceTheStopBegan(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			var own drain.Owner
			run := own.NewRun()
			admit := admission(run, interceptors.InterceptorPair{
				Unary: func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
					return h(ctx, req)
				},
				Stream: func(srv any, ss grpcgo.ServerStream, _ *grpcgo.StreamServerInfo, h grpcgo.StreamHandler) error {
					return h(srv, ss)
				},
			})
			call := func(h func()) error {
				if kind == "unary" {
					_, err := admit.Unary(context.Background(), nil, &grpcgo.UnaryServerInfo{}, func(context.Context, any) (any, error) {
						h()
						return nil, nil
					})
					return err
				}
				return admit.Stream(nil, nil, &grpcgo.StreamServerInfo{}, func(any, grpcgo.ServerStream) error {
					h()
					return nil
				})
			}

			idleDuringCall := true
			if err := call(func() {
				run.Close() // the stop begins while the call runs
				idleDuringCall = drain.Closed(run.Idle())
			}); err != nil {
				t.Fatalf("admitted call = %v", err)
			}
			if idleDuringCall {
				t.Error("the run went idle while an admitted call ran")
			}
			if !drain.Closed(run.Idle()) {
				t.Error("the run is not idle once the call returned")
			}

			ran := false
			err := call(func() { ran = true })
			if ran {
				t.Error("a call that arrived after the stop began ran")
			}
			if st, _ := status.FromError(err); st.Code() != codes.Unavailable || st.Message() != "server is stopping" {
				t.Errorf("refused call = %v, want Unavailable \"server is stopping\"", err)
			}
		})
	}
}

// BenchmarkAdmission measures what admission adds to a call: the
// outermost interceptor called directly, and the same interceptor behind
// admission into a run, on one goroutine and on every core at once.
func BenchmarkAdmission(b *testing.B) {
	outer := interceptors.InterceptorPair{
		Unary: func(ctx context.Context, req any, _ *grpcgo.UnaryServerInfo, h grpcgo.UnaryHandler) (any, error) {
			return h(ctx, req)
		},
	}
	info := &grpcgo.UnaryServerInfo{FullMethod: "/svc.Bench/Do"}
	h := func(context.Context, any) (any, error) { return nil, nil }
	ctx := context.Background()
	var own drain.Owner
	for name, ic := range map[string]grpcgo.UnaryServerInterceptor{
		"without": outer.Unary,
		"with":    admission(own.NewRun(), outer).Unary,
	} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = ic(ctx, nil, info, h)
			}
		})
		// Calls on every core admitted into the one run at once.
		b.Run(name+"/parallel", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_, _ = ic(ctx, nil, info, h)
				}
			})
		})
	}
}

// A gateway request admitted before the stop began holds the run until
// the handler returns; a request that arrives after is refused with 503
// and never reaches the handler.
func TestAdmitRequests_HoldsTheRunAndRefusesOnceTheStopBegan(t *testing.T) {
	var own drain.Owner
	run := own.NewRun()
	idleDuringRequest, ran := true, false
	first := true
	h := admitRequests(run, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		ran = true
		if first {
			run.Close() // the stop begins while the request runs
			idleDuringRequest = drain.Closed(run.Idle())
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if idleDuringRequest {
		t.Error("the run went idle while an admitted request ran")
	}
	if !drain.Closed(run.Idle()) {
		t.Error("the run is not idle once the request returned")
	}

	first, ran = false, false
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if ran {
		t.Error("a request that arrived after the stop began ran")
	}
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != "server is stopping" {
		t.Errorf("refused request = %d %q, want 503 \"server is stopping\"", rec.Code, rec.Body.String())
	}
}
