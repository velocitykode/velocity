package interceptors_test

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/trace"
)

// layerReports records the errors it receives.
type layerReports struct {
	mu   sync.Mutex
	errs []error
}

func (r *layerReports) Report(err error, _ *contract.ErrorContext) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
}

func (r *layerReports) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// chainUnary runs the interceptors in order around handler, as grpc-go's
// chained interceptor does.
func chainUnary(ctx context.Context, handler grpc.UnaryHandler, ics ...grpc.UnaryServerInterceptor) (any, error) {
	info := mockUnaryServerInfo("/svc.Work/Do")
	h := handler
	for i := len(ics) - 1; i >= 0; i-- {
		ic, next := ics[i], h
		h = func(ctx context.Context, req any) (any, error) { return ic(ctx, req, info, next) }
	}
	return h(ctx, nil)
}

// chainStream runs the stream interceptors in order around handler.
func chainStream(ss grpc.ServerStream, handler grpc.StreamHandler, ics ...grpc.StreamServerInterceptor) error {
	info := mockStreamServerInfo("/svc.Work/Watch")
	h := handler
	for i := len(ics) - 1; i >= 0; i-- {
		ic, next := ics[i], h
		h = func(srv any, ss grpc.ServerStream) error { return ic(srv, ss, info, next) }
	}
	return h(nil, ss)
}

// One recovery pair at both ends of a chain: a handler panic is recovered
// by the last layer, so the logging interceptor between them ends the call
// with the code the panic became (the PanicHandler's, when set); a panic in
// an interceptor between them is recovered by the first layer. Each panic
// is reported once and never again as an internal error.
func TestRecovery_BothEndsOfTheChain(t *testing.T) {
	cases := []struct {
		name         string
		panicHandler func(context.Context, any) error
		middlePanics bool
		wantCode     codes.Code
		wantEvents   []codes.Code
	}{
		{name: "handler panic", wantCode: codes.Internal, wantEvents: []codes.Code{codes.Internal, codes.Internal}},
		{
			name:         "handler panic with a panic handler",
			panicHandler: func(context.Context, any) error { return status.Error(codes.Unavailable, "retry") },
			wantCode:     codes.Unavailable,
			wantEvents:   []codes.Code{codes.Unavailable, codes.Unavailable},
		},
		{name: "interceptor panic", middlePanics: true, wantCode: codes.Internal},
	}
	for _, tc := range cases {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				reports := &layerReports{}
				opts := []interceptors.RecoveryOption{interceptors.WithRecoveryReporter(reports), interceptors.WithStackTrace(false)}
				if tc.panicHandler != nil {
					opts = append(opts, interceptors.WithPanicHandler(tc.panicHandler))
				}
				rec := interceptors.Recovery(opts...)
				evs := &statusEvents{}
				logging := interceptors.Logging(interceptors.WithLoggingLogger(newBoundLogger()), interceptors.WithEventDispatcher(evs.dispatch))
				middleUnary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
					if tc.middlePanics {
						panic("interceptor broke")
					}
					return h(ctx, req)
				}
				middleStream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
					if tc.middlePanics {
						panic("interceptor broke")
					}
					return h(srv, ss)
				}

				var err error
				if kind == "unary" {
					_, err = chainUnary(context.Background(), func(context.Context, any) (any, error) { panic("handler broke") },
						rec.Unary, logging.Unary, middleUnary, rec.Unary)
				} else {
					err = chainStream(&mockServerStream{ctx: context.Background()}, func(any, grpc.ServerStream) error { panic("handler broke") },
						rec.Stream, logging.Stream, middleStream, rec.Stream)
				}
				if got := status.Code(err); got != tc.wantCode {
					t.Fatalf("code = %v (%v), want %v", got, err, tc.wantCode)
				}
				if n := reports.count(); n != 1 {
					t.Errorf("reports = %d, want 1", n)
				}
				evs.mu.Lock()
				got := append([]codes.Code(nil), evs.codes...)
				evs.mu.Unlock()
				if tc.middlePanics {
					// The panic unwound the logging interceptor: the
					// outer layer only keeps the server alive.
					return
				}
				if len(got) != len(tc.wantEvents) || got[0] != tc.wantEvents[0] || got[1] != tc.wantEvents[1] {
					t.Errorf("failed and completed StatusCode = %v, want %v", got, tc.wantEvents)
				}
			})
		}
	}
}

// Correlation gives the call its ids once: a Logging after it runs the
// handler under the same span and request id, while an interceptor between
// them that puts a different trace in the context makes Logging start a
// new span under that trace, as it does for any earlier trace.
func TestCorrelation_LoggingReusesItsSpan(t *testing.T) {
	corr := interceptors.Correlation()
	logging := interceptors.Logging(interceptors.WithLoggingLogger(newBoundLogger()))
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			var outerSpan, outerReq, innerSpan, innerReq, innerTrace string
			observe := func(ctx context.Context) {
				outerSpan, outerReq = trace.GetSpanID(ctx), trace.GetRequestID(ctx)
			}
			record := func(ctx context.Context) {
				innerSpan, innerReq, innerTrace = trace.GetSpanID(ctx), trace.GetRequestID(ctx), trace.GetTraceID(ctx)
			}
			run := func(retrace bool) {
				if kind == "unary" {
					mid := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
						observe(ctx)
						if retrace {
							ctx = trace.WithTrace(ctx, "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")
						}
						return h(ctx, req)
					}
					_, _ = chainUnary(context.Background(), func(ctx context.Context, _ any) (any, error) { record(ctx); return nil, nil },
						corr.Unary, mid, logging.Unary)
					return
				}
				mid := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
					observe(ss.Context())
					if retrace {
						ss = &mockServerStream{ctx: trace.WithTrace(ss.Context(), "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")}
					}
					return h(srv, ss)
				}
				_ = chainStream(&mockServerStream{ctx: context.Background()}, func(_ any, ss grpc.ServerStream) error { record(ss.Context()); return nil },
					corr.Stream, mid, logging.Stream)
			}

			run(false)
			if outerSpan == "" || outerReq == "" {
				t.Fatalf("Correlation gave no ids: span %q request %q", outerSpan, outerReq)
			}
			if innerSpan != outerSpan || innerReq != outerReq {
				t.Errorf("handler ran under span %q request %q, want Correlation's %q %q", innerSpan, innerReq, outerSpan, outerReq)
			}

			run(true)
			if innerTrace != "0af7651916cd43dd8448eb211c80319c" || innerSpan == "b7ad6b7169203331" || innerSpan == "" {
				t.Errorf("after a re-trace the handler ran under trace %q span %q, want a new span under the new trace", innerTrace, innerSpan)
			}
			if innerReq != outerReq {
				t.Errorf("request id = %q, want the kept %q", innerReq, outerReq)
			}
		})
	}
}
