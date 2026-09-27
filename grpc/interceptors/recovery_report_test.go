package interceptors

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// reportLog records every report it receives.
type reportLog struct {
	mu   sync.Mutex
	errs []error
	ctxs []*contract.ErrorContext
}

func (r *reportLog) Report(err error, ctx *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errs = append(r.errs, err)
	r.ctxs = append(r.ctxs, ctx)
}

func (r *reportLog) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.errs)
}

// errorLines is a logger that counts the lines written at error level.
type errorLines struct {
	mu sync.Mutex
	n  int
}

func (l *errorLines) Debug(string, ...any) {}
func (l *errorLines) Info(string, ...any)  {}
func (l *errorLines) Warn(string, ...any)  {}
func (l *errorLines) Fatal(string, ...any) {}
func (l *errorLines) Error(string, ...any) {
	l.mu.Lock()
	l.n++
	l.mu.Unlock()
}

func (l *errorLines) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.n
}

// TestRecovery_ReportsPanicOnce asserts a panic the recovery interceptor
// recovers is reported once to its reporter, as a recovered panic naming
// the method and carrying the call's request and trace IDs and the stack,
// instead of being logged; the client gets the same codes.Internal status.
func TestRecovery_ReportsPanicOnce(t *testing.T) {
	reports := &reportLog{}
	logger := &errorLines{}
	pair := Recovery(WithRecoveryReporter(reports), WithRecoveryLogger(logger))

	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), "trace-grpc", "span-grpc"), "req-grpc")
	handler := func(context.Context, interface{}) (interface{}, error) { panic("handler exploded") }
	_, err := pair.Unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.Orders/Ship"}, handler)
	if status.Code(err) != codes.Internal {
		t.Fatalf("status = %v, want codes.Internal", status.Code(err))
	}
	if reports.count() != 1 {
		t.Fatalf("reports = %d, want 1", reports.count())
	}
	var rp contract.RecoveredPanic
	if !errors.As(reports.errs[0], &rp) || rp.Recovered() != "handler exploded" {
		t.Errorf("reported %#v, want the recovered panic", reports.errs[0])
	}
	exCtx := reports.ctxs[0]
	if exCtx.Extra["method"] != "/svc.Orders/Ship" {
		t.Errorf("report method = %v, want /svc.Orders/Ship", exCtx.Extra["method"])
	}
	if !exCtx.Recovered || exCtx.PanicStack == "" {
		t.Errorf("report context = %+v, want a recovered panic with its stack", exCtx)
	}
	if exCtx.RequestID != "req-grpc" || exCtx.TraceID != "trace-grpc" || exCtx.SpanID != "span-grpc" {
		t.Errorf("report IDs = %q/%q/%q, want the call's", exCtx.RequestID, exCtx.TraceID, exCtx.SpanID)
	}
	if logger.count() != 0 {
		t.Errorf("reported panic also logged %d times, want 0", logger.count())
	}

	streamHandler := func(interface{}, grpc.ServerStream) error { panic("stream exploded") }
	serr := pair.Stream(nil, &fakeStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/svc.Orders/Watch"}, streamHandler)
	if status.Code(serr) != codes.Internal {
		t.Fatalf("stream status = %v, want codes.Internal", status.Code(serr))
	}
	if reports.count() != 2 || reports.ctxs[1].Extra["method"] != "/svc.Orders/Watch" {
		t.Errorf("stream panic not reported once with its method: %d reports", reports.count())
	}
}

// TestRecovery_ReportsInternalHandlerErrors asserts an internal error a
// handler returns (codes.Internal or codes.Unknown, a plain Go error
// included) is reported once with the method named, the client getting the
// same error, while a client-outcome status and a call ended by its own
// context are not reported.
func TestRecovery_ReportsInternalHandlerErrors(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	plain := errors.New("database unreachable")
	tests := []struct {
		name       string
		ctx        context.Context
		err        error
		wantReport bool
	}{
		{"plain error", context.Background(), plain, true},
		{"internal status", context.Background(), status.Error(codes.Internal, "broken"), true},
		{"unknown status", context.Background(), status.Error(codes.Unknown, "odd"), true},
		{"not found", context.Background(), status.Error(codes.NotFound, "no order"), false},
		{"invalid argument", context.Background(), status.Error(codes.InvalidArgument, "bad id"), false},
		{"context ended the call", cancelled, context.Canceled, false},
		{"no error", context.Background(), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reports := &reportLog{}
			pair := Recovery(WithRecoveryReporter(reports))
			handler := func(context.Context, interface{}) (interface{}, error) { return nil, tt.err }
			_, err := pair.Unary(tt.ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc.Orders/Get"}, handler)
			if err != tt.err {
				t.Fatalf("returned %v, want the handler's error unchanged", err)
			}
			want := 0
			if tt.wantReport {
				want = 1
			}
			if reports.count() != want {
				t.Fatalf("reports = %d, want %d", reports.count(), want)
			}
			if tt.wantReport {
				if reports.errs[0] != tt.err {
					t.Errorf("reported %#v, want the handler's error", reports.errs[0])
				}
				if reports.ctxs[0].Extra["method"] != "/svc.Orders/Get" || reports.ctxs[0].Recovered {
					t.Errorf("report context = %+v, want the method named, not a panic", reports.ctxs[0])
				}
			}

			streamHandler := func(interface{}, grpc.ServerStream) error { return tt.err }
			if serr := pair.Stream(nil, &fakeStream{ctx: tt.ctx}, &grpc.StreamServerInfo{FullMethod: "/svc.Orders/Watch"}, streamHandler); serr != tt.err {
				t.Fatalf("stream returned %v, want the handler's error unchanged", serr)
			}
			if reports.count() != 2*want {
				t.Errorf("stream reports = %d, want %d", reports.count()-want, want)
			}
		})
	}
}

// panickingReporter panics on every report.
type panickingReporter struct{}

func (panickingReporter) Report(error, *contract.ErrorContext) { panic("reporter exploded") }

// TestRecovery_PanickingReporterContained asserts a reporter that panics
// cannot crash the call: the client still gets codes.Internal for a panic
// and the handler's own error otherwise.
func TestRecovery_PanickingReporterContained(t *testing.T) {
	pair := Recovery(WithRecoveryReporter(panickingReporter{}))
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Orders/Ship"}
	_, err := pair.Unary(context.Background(), nil, info, func(context.Context, interface{}) (interface{}, error) { panic("boom") })
	if status.Code(err) != codes.Internal {
		t.Errorf("status = %v, want codes.Internal", status.Code(err))
	}
	plain := errors.New("database unreachable")
	_, err = pair.Unary(context.Background(), nil, info, func(context.Context, interface{}) (interface{}, error) { return nil, plain })
	if err != plain {
		t.Errorf("returned %v, want the handler's error", err)
	}
}

// fakeStream is a grpc.ServerStream carrying ctx.
type fakeStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeStream) Context() context.Context { return s.ctx }
