package interceptors_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/interceptors"
)

func panickingUnary(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
	panic("interceptor broke")
}

func panickingStream(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
	panic("interceptor broke")
}

// Without a CallLifecycle owner in the context, ContainUnary and
// ContainStream contain nothing: the panic continues unchanged.
func TestContain_WithoutAnOwnerThePanicContinues(t *testing.T) {
	cases := map[string]func(){
		"unary": func() {
			_, _ = interceptors.ContainUnary(panickingUnary)(context.Background(), nil, mockUnaryServerInfo("/svc/Do"), nil)
		},
		"stream": func() {
			_ = interceptors.ContainStream(panickingStream)(nil, &mockServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/svc/Do"}, nil)
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if p := recover(); p != "interceptor broke" {
					t.Errorf("recovered %v, want the interceptor's own panic", p)
				}
			}()
			run()
			t.Error("returned; want the panic to continue")
		})
	}
}

// ctxMark marks the context an interceptor passes on.
type ctxMark struct{}

// A contained interceptor's panic reaches the interceptors above it as the
// error its continuation returns, never as a panic: codes.Internal, or
// the PanicHandler's result, which receives the context the contained
// interceptor was called with. The call is reported once and ends with
// its terminal sequence, as for a handler panic.
func TestContain_UpstreamSeesThePanicAsAnError(t *testing.T) {
	custom := errors.New("custom")
	for _, tc := range []struct {
		name    string
		handler func(context.Context, any) error
		want    func(error) bool
	}{
		{name: "internal", want: func(err error) bool { return status.Code(err) == codes.Internal }},
		{name: "panic handler", handler: func(ctx context.Context, _ any) error {
			if ctx.Value(ctxMark{}) != true {
				return errors.New("panic handler got another context")
			}
			return custom
		}, want: func(err error) bool { return errors.Is(err, custom) }},
	} {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				reports := &layerReports{}
				evs := &callEvents{}
				opts := []interceptors.CallOption{interceptors.WithReporter(reports), interceptors.WithStackTrace(false), interceptors.WithEventDispatcher(evs.dispatch)}
				if tc.handler != nil {
					opts = append(opts, interceptors.WithPanicHandler(tc.handler))
				}
				calls := interceptors.CallLifecycle(opts...)
				var seen error
				var err error
				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("a panic reached the caller: %v", p)
						}
					}()
					if kind == "unary" {
						upstream := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
							_, seen = h(context.WithValue(ctx, ctxMark{}, true), req)
							return nil, seen
						}
						_, err = chainUnary(context.Background(), func(context.Context, any) (any, error) { return nil, nil },
							calls.Unary, interceptors.ContainUnary(upstream), interceptors.ContainUnary(panickingUnary), calls.Unary)
						return
					}
					upstream := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
						seen = h(srv, &mockServerStream{ctx: context.WithValue(ss.Context(), ctxMark{}, true)})
						return seen
					}
					err = chainStream(&mockServerStream{ctx: context.Background()}, func(any, grpc.ServerStream) error { return nil },
						calls.Stream, interceptors.ContainStream(upstream), interceptors.ContainStream(panickingStream), calls.Stream)
				}()
				if !tc.want(seen) || !tc.want(err) {
					t.Errorf("upstream saw %v, call ended %v", seen, err)
				}
				if reports.count() != 1 {
					t.Errorf("reports = %d, want 1", reports.count())
				}
				kinds, _, _ := evs.snapshot()
				if got := terminal(kinds); !equalKinds(got, []string{"started", "failed", "completed"}) {
					t.Errorf("lifecycle events = %v, want one terminal sequence", got)
				}
			})
		}
	}
}
