package interceptors_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/trace"
)

// callEvents records the lifecycle events of calls, with the span each
// was dispatched under.
type callEvents struct {
	mu    sync.Mutex
	kinds []string
	codes []codes.Code
	spans []string
}

func (e *callEvents) dispatch(ctx context.Context, ev any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	kind, code := "", codes.OK
	switch v := ev.(type) {
	case *grpcevents.RequestStarted, *grpcevents.StreamStarted:
		kind = "started"
	case *grpcevents.RequestFailed:
		kind, code = "failed", v.StatusCode
	case *grpcevents.StreamFailed:
		kind, code = "failed", v.StatusCode
	case *grpcevents.RequestCompleted:
		kind, code = "completed", v.StatusCode
	case *grpcevents.StreamCompleted:
		kind, code = "completed", v.StatusCode
	case *grpcevents.PanicRecovered:
		kind = "panic"
	default:
		return nil
	}
	e.kinds = append(e.kinds, kind)
	e.codes = append(e.codes, code)
	e.spans = append(e.spans, trace.GetSpanID(ctx))
	return nil
}

func (e *callEvents) snapshot() (kinds []string, cs []codes.Code, spans []string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.kinds...), append([]codes.Code(nil), e.codes...), append([]string(nil), e.spans...)
}

// terminal returns the kinds without PanicRecovered.
func terminal(kinds []string) []string {
	var out []string
	for _, k := range kinds {
		if k != "panic" {
			out = append(out, k)
		}
	}
	return out
}

func equalKinds(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// runChain runs one call, unary or stream, through first, middle and last
// around handler, and returns the error the call ended with.
func runChain(kind string, calls interceptors.InterceptorPair, middle func(next func(ctx context.Context) error, ctx context.Context) error, handler func(ctx context.Context) error) error {
	if kind == "unary" {
		mid := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			var resp any
			err := middle(func(ctx context.Context) error {
				var err error
				resp, err = h(ctx, req)
				return err
			}, ctx)
			return resp, err
		}
		_, err := chainUnary(context.Background(), func(ctx context.Context, _ any) (any, error) { return nil, handler(ctx) },
			calls.Unary, mid, calls.Unary)
		return err
	}
	mid := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		return middle(func(ctx context.Context) error { return h(srv, &mockServerStream{ctx: ctx}) }, ss.Context())
	}
	return chainStream(&mockServerStream{ctx: context.Background()}, func(_ any, ss grpc.ServerStream) error { return handler(ss.Context()) },
		calls.Stream, mid, calls.Stream)
}

// passThrough is a middle interceptor that runs the rest of the chain.
func passThrough(next func(ctx context.Context) error, ctx context.Context) error { return next(ctx) }

// Every started call gets exactly one terminal sequence (request line,
// then failed if it failed, then completed) and at most one error report,
// all under the call's own span, however it ends: a returned error, a
// handler panic, or a panic in an interceptor between the two ends of the
// chain. A PanicHandler's result is the call's result, nil included.
func TestCallLifecycle_EveryStartedCallEndsOnce(t *testing.T) {
	cases := []struct {
		name         string
		middle       func(next func(context.Context) error, ctx context.Context) error
		handler      func(context.Context) error
		panicHandler func(context.Context, any) error
		wantCode     codes.Code
		wantKinds    []string
		wantReports  int
	}{
		{name: "success", middle: passThrough, handler: func(context.Context) error { return nil },
			wantCode: codes.OK, wantKinds: []string{"started", "completed"}},
		{name: "internal error", middle: passThrough, handler: func(context.Context) error { return status.Error(codes.Internal, "broke") },
			wantCode: codes.Internal, wantKinds: []string{"started", "failed", "completed"}, wantReports: 1},
		{name: "not found", middle: passThrough, handler: func(context.Context) error { return status.Error(codes.NotFound, "none") },
			wantCode: codes.NotFound, wantKinds: []string{"started", "failed", "completed"}},
		{name: "handler panic", middle: passThrough, handler: func(context.Context) error { panic("handler broke") },
			wantCode: codes.Internal, wantKinds: []string{"started", "failed", "completed"}, wantReports: 1},
		{name: "interceptor panic", middle: func(func(context.Context) error, context.Context) error { panic("interceptor broke") },
			handler:  func(context.Context) error { return nil },
			wantCode: codes.Internal, wantKinds: []string{"started", "failed", "completed"}, wantReports: 1},
		{name: "interceptor panic after the handler", middle: func(next func(context.Context) error, ctx context.Context) error {
			_ = next(ctx)
			panic("interceptor broke late")
		}, handler: func(context.Context) error { return nil },
			wantCode: codes.Internal, wantKinds: []string{"started", "failed", "completed"}, wantReports: 1},
		{name: "panic handler returns a status", middle: passThrough, handler: func(context.Context) error { panic("handler broke") },
			panicHandler: func(context.Context, any) error { return status.Error(codes.Unavailable, "retry") },
			wantCode:     codes.Unavailable, wantKinds: []string{"started", "failed", "completed"}, wantReports: 1},
		{name: "panic handler returns nil", middle: passThrough, handler: func(context.Context) error { panic("handler broke") },
			panicHandler: func(context.Context, any) error { return nil },
			wantCode:     codes.OK, wantKinds: []string{"started", "completed"}, wantReports: 1},
		{name: "interceptor panic with a panic handler returning nil", middle: func(func(context.Context) error, context.Context) error { panic("interceptor broke") },
			handler:      func(context.Context) error { return nil },
			panicHandler: func(context.Context, any) error { return nil },
			wantCode:     codes.OK, wantKinds: []string{"started", "completed"}, wantReports: 1},
	}
	for _, tc := range cases {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				reports := &layerReports{}
				evs := &callEvents{}
				lines := newBoundLogger()
				opts := []interceptors.CallOption{
					interceptors.WithReporter(reports), interceptors.WithStackTrace(false),
					interceptors.WithLogger(lines), interceptors.WithRequestLine(),
					interceptors.WithEventDispatcher(evs.dispatch),
				}
				if tc.panicHandler != nil {
					opts = append(opts, interceptors.WithPanicHandler(tc.panicHandler))
				}
				calls := interceptors.CallLifecycle(opts...)

				var handlerSpan string
				err := runChain(kind, calls, tc.middle, func(ctx context.Context) error {
					handlerSpan = trace.GetSpanID(ctx)
					return tc.handler(ctx)
				})
				if got := status.Code(err); got != tc.wantCode {
					t.Fatalf("code = %v (%v), want %v", got, err, tc.wantCode)
				}
				kinds, cs, spans := evs.snapshot()
				if got := terminal(kinds); !equalKinds(got, tc.wantKinds) {
					t.Fatalf("lifecycle events = %v, want %v", got, tc.wantKinds)
				}
				if last := cs[len(cs)-1]; last != tc.wantCode {
					t.Errorf("completed StatusCode = %v, want %v", last, tc.wantCode)
				}
				if n := reports.count(); n != tc.wantReports {
					t.Errorf("reports = %d, want %d", n, tc.wantReports)
				}

				// One span for everything: the request line, every event
				// and the report; the handler runs under it too unless an
				// interceptor panicked before reaching it.
				line := lines.last(t)
				callSpan, _ := line["span_id"].(string)
				if callSpan == "" {
					t.Fatalf("request line has no span: %v", line)
				}
				if handlerSpan != "" && handlerSpan != callSpan {
					t.Errorf("handler span %q, want the call's %q", handlerSpan, callSpan)
				}
				for i, s := range spans {
					if s != callSpan {
						t.Errorf("event %s under span %q, want %q", kinds[i], s, callSpan)
					}
				}
				for _, ec := range reports.contexts() {
					if ec.SpanID != callSpan || ec.RequestID != line["request_id"] {
						t.Errorf("report ids %q/%q, want the request line's %q/%v", ec.RequestID, ec.SpanID, callSpan, line["request_id"])
					}
				}
			})
		}
	}
}

// An interceptor inside the call that re-traces the context (a tracing
// middleware) does not split the call: the request line, the events and
// the report of a failed call keep the call's one span, and the handler
// runs under the new trace. A re-trace before the call lifecycle interceptor, in a
// chain built by hand, becomes the call's trace for all of them.
func TestCallLifecycle_ReportSharesTheRequestLineCorrelation(t *testing.T) {
	const appTrace, appSpan = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	retrace := func(next func(context.Context) error, ctx context.Context) error {
		return next(trace.WithTrace(ctx, appTrace, appSpan))
	}
	internal := func(context.Context) error { return status.Error(codes.Internal, "broke") }
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind+"/inside", func(t *testing.T) {
			reports := &layerReports{}
			lines := newBoundLogger()
			calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithLogger(lines), interceptors.WithRequestLine())
			var handlerTrace string
			_ = runChain(kind, calls, retrace, func(ctx context.Context) error {
				handlerTrace = trace.GetTraceID(ctx)
				return internal(ctx)
			})
			line := lines.last(t)
			ecs := reports.contexts()
			if len(ecs) != 1 {
				t.Fatalf("reports = %d, want 1", len(ecs))
			}
			if ecs[0].TraceID != line["trace_id"] || ecs[0].SpanID != line["span_id"] {
				t.Errorf("report %s/%s, want the request line's %v/%v", ecs[0].TraceID, ecs[0].SpanID, line["trace_id"], line["span_id"])
			}
			if handlerTrace != appTrace {
				t.Errorf("handler trace %q, want the re-traced %q", handlerTrace, appTrace)
			}
		})
		t.Run(kind+"/before", func(t *testing.T) {
			reports := &layerReports{}
			lines := newBoundLogger()
			calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithLogger(lines), interceptors.WithRequestLine())
			var err error
			if kind == "unary" {
				pre := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
					return h(trace.WithTrace(ctx, appTrace, appSpan), req)
				}
				_, err = chainUnary(context.Background(), func(ctx context.Context, _ any) (any, error) { return nil, internal(ctx) }, pre, calls.Unary, calls.Unary)
			} else {
				pre := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
					return h(srv, &mockServerStream{ctx: trace.WithTrace(ss.Context(), appTrace, appSpan)})
				}
				err = chainStream(&mockServerStream{ctx: context.Background()}, func(_ any, ss grpc.ServerStream) error { return internal(ss.Context()) }, pre, calls.Stream, calls.Stream)
			}
			if status.Code(err) != codes.Internal {
				t.Fatalf("code = %v", status.Code(err))
			}
			line := lines.last(t)
			ecs := reports.contexts()
			if len(ecs) != 1 || ecs[0].TraceID != appTrace || line["trace_id"] != appTrace || ecs[0].SpanID != line["span_id"] {
				t.Errorf("reports %+v, line %v: want one report and the line under %s with one span", ecs, line, appTrace)
			}
		})
	}
}

// A call reports at most one error even when an interceptor replaces the
// error a panic became with its own Internal status, dropping the original
// (no %w): the panic claimed the call's report first.
func TestCallLifecycle_ReplacedPanicErrorIsNotReportedAgain(t *testing.T) {
	mask := func(next func(context.Context) error, ctx context.Context) error {
		if err := next(ctx); err != nil {
			return status.Error(codes.Internal, "masked")
		}
		return nil
	}
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			reports := &layerReports{}
			calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false))
			err := runChain(kind, calls, mask, func(context.Context) error { panic("handler broke") })
			if status.Convert(err).Message() != "masked" {
				t.Fatalf("err = %v, want the interceptor's", err)
			}
			if n := reports.count(); n != 1 {
				t.Fatalf("reports = %d, want 1", n)
			}
			var rp contract.RecoveredPanic
			if !errors.As(reports.errs[0], &rp) {
				t.Errorf("reported %v, want the recovered panic", reports.errs[0])
			}
		})
	}
}

// timeoutMiddle runs the rest of the chain on a goroutine of its own and
// returns code as soon as release is closed, without waiting for it; the
// goroutine closes finished when the rest of the chain returned.
func timeoutMiddle(code codes.Code, release <-chan struct{}, finished chan<- struct{}) func(next func(context.Context) error, ctx context.Context) error {
	return func(next func(context.Context) error, ctx context.Context) error {
		go func() {
			defer close(finished)
			_ = next(ctx)
		}()
		<-release
		return status.Error(code, "timed out")
	}
}

// A handler panic on a goroutine an interceptor started, after that
// interceptor returned and the call ended, is contained on that goroutine
// (the process survives), reported once as late work (Extra "late") under
// the call's span, and produces no second set of terminal events. When
// the call itself ended with an internal error, that error keeps its own
// report.
func TestCallLifecycle_LatePanicIsReportedAsLateWork(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        codes.Code
		wantReports int
	}{
		{name: "call ended with DeadlineExceeded", code: codes.DeadlineExceeded, wantReports: 1},
		{name: "call ended with Internal", code: codes.Internal, wantReports: 2},
	} {
		for _, kind := range []string{"unary", "stream"} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				reports := &layerReports{}
				evs := &callEvents{}
				calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false), interceptors.WithEventDispatcher(evs.dispatch))
				release, finished := make(chan struct{}), make(chan struct{})
				handlerMay := make(chan struct{})
				close(release)
				go func() {
					// Let the handler panic only after the call ended.
					time.Sleep(20 * time.Millisecond)
					close(handlerMay)
				}()
				err := runChain(kind, calls, timeoutMiddle(tc.code, release, finished), func(context.Context) error {
					<-handlerMay
					panic("late panic")
				})
				if status.Code(err) != tc.code {
					t.Fatalf("code = %v, want %v", status.Code(err), tc.code)
				}
				<-finished

				kinds, _, spans := evs.snapshot()
				if got := terminal(kinds); !equalKinds(got, []string{"started", "failed", "completed"}) {
					t.Errorf("lifecycle events = %v, want one terminal sequence", got)
				}
				ecs := reports.contexts()
				if len(ecs) != tc.wantReports {
					t.Fatalf("reports = %d, want %d", len(ecs), tc.wantReports)
				}
				late := ecs[len(ecs)-1]
				if late.Extra["late"] != true || !late.Recovered {
					t.Errorf("last report %+v, want the recovered panic marked late", late)
				}
				if late.SpanID != spans[0] {
					t.Errorf("late report under span %q, want the call's %q", late.SpanID, spans[0])
				}
			})
		}
	}
}

// Many calls whose interceptor returns while its goroutine's handler
// panics at any moment race the owner ending the call against the
// containing layer: under -race there is no data race, the process
// survives, each call gets exactly one terminal sequence, and each panic
// is reported exactly once (in the call, or as late work), never lost.
func TestCallLifecycle_ConcurrentLatePanicsRaceTheOwner(t *testing.T) {
	const n = 200
	reports := &layerReports{}
	evs := &callEvents{}
	calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false), interceptors.WithEventDispatcher(evs.dispatch))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, finished := make(chan struct{}), make(chan struct{})
			close(release)
			kind := []string{"unary", "stream"}[i%2]
			_ = runChain(kind, calls, timeoutMiddle(codes.DeadlineExceeded, release, finished), func(context.Context) error {
				if i%3 == 0 {
					time.Sleep(time.Duration(i%5) * time.Microsecond)
				}
				panic(fmt.Sprintf("panic %d", i))
			})
			<-finished
		}(i)
	}
	wg.Wait()
	kinds, _, _ := evs.snapshot()
	var completed int
	for _, k := range kinds {
		if k == "completed" {
			completed++
		}
	}
	if completed != n {
		t.Errorf("completed events = %d, want %d", completed, n)
	}
	if got := reports.count(); got != n {
		t.Errorf("reports = %d, want %d: one per panic", got, n)
	}
}

// hostileLogger panics in every method.
type hostileLogger struct{}

func (hostileLogger) Debug(string, ...any)                    { panic("logger broke") }
func (hostileLogger) Info(string, ...any)                     { panic("logger broke") }
func (hostileLogger) Warn(string, ...any)                     { panic("logger broke") }
func (hostileLogger) Error(string, ...any)                    { panic("logger broke") }
func (hostileLogger) Fatal(string, ...any)                    { panic("logger broke") }
func (hostileLogger) With(...any) contract.Logger             { panic("logger broke") }
func (hostilerReporter) Report(error, *contract.ErrorContext) { panic("reporter broke") }

type hostilerReporter struct{}

// User code the call lifecycle interceptor calls when a call ends (the logger, the
// reporter, the event dispatcher, ExtraFields and the PanicHandler) may
// panic: each is contained, the call still ends with its terminal
// sequence, and the process survives.
func TestCallLifecycle_PanickingUserCodeNeverSkipsTheEnd(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			evs := &callEvents{}
			var dispatched int
			calls := interceptors.CallLifecycle(
				interceptors.WithLogger(hostileLogger{}), interceptors.WithRequestLine(),
				interceptors.WithReporter(hostilerReporter{}), interceptors.WithStackTrace(false),
				interceptors.WithExtraFields(func(context.Context) []any { panic("extra fields broke") }),
				interceptors.WithPanicHandler(func(context.Context, any) error { panic("panic handler broke") }),
				interceptors.WithEventDispatcher(func(ctx context.Context, ev any) error {
					_ = evs.dispatch(ctx, ev)
					dispatched++
					panic("dispatcher broke")
				}),
			)
			var err error
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("a panic escaped the call: %v", p)
					}
				}()
				err = runChain(kind, calls, passThrough, func(context.Context) error { panic("handler broke") })
			}()
			if status.Code(err) != codes.Internal {
				t.Errorf("code = %v, want Internal (a panicking PanicHandler)", status.Code(err))
			}
			kinds, _, _ := evs.snapshot()
			if got := terminal(kinds); !equalKinds(got, []string{"started", "failed", "completed"}) {
				t.Errorf("lifecycle events = %v, want one terminal sequence", got)
			}
		})
	}
}

// A skipped method (SkipMethods, health checks) gets no request line and
// no lifecycle events, but is still correlated, recovered and reported.
func TestCallLifecycle_SkippedMethodIsStillRecoveredAndReported(t *testing.T) {
	reports := &layerReports{}
	evs := &callEvents{}
	lines := newBoundLogger()
	calls := interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(false), interceptors.WithLogger(lines),
		interceptors.WithRequestLine(), interceptors.WithEventDispatcher(evs.dispatch), interceptors.WithSkipMethods("/svc.Work/Do"))
	var span string
	_, err := chainUnary(context.Background(), func(ctx context.Context, _ any) (any, error) {
		span = trace.GetSpanID(ctx)
		panic("broke")
	}, calls.Unary, calls.Unary)
	if status.Code(err) != codes.Internal || span == "" {
		t.Fatalf("code = %v, span = %q: want a correlated call that ended Internal", status.Code(err), span)
	}
	if reports.count() != 1 || reports.contexts()[0].SpanID != span {
		t.Errorf("reports = %d, want 1 under the call's span", reports.count())
	}
	kinds, _, _ := evs.snapshot()
	if got := terminal(kinds); len(got) != 0 {
		t.Errorf("lifecycle events = %v, want none", got)
	}
	lines.sink.mu.Lock()
	defer lines.sink.mu.Unlock()
	for _, l := range lines.sink.lines {
		if l["code"] != nil {
			t.Errorf("request line %v written for a skipped method", l)
		}
	}
}

// An interceptor that answers without calling the handler (an Auth
// rejection, say) publishes no handler context: the request line and the
// events read user fields from the owner's own context, which carries
// whatever came before the call lifecycle interceptor.
func TestCallLifecycle_ShortCircuitReadsUserFieldsFromTheOwner(t *testing.T) {
	lines := newBoundLogger()
	evs := &eventCollector{}
	calls := interceptors.CallLifecycle(interceptors.WithLogger(lines), interceptors.WithRequestLine(), interceptors.WithEventDispatcher(evs.dispatch))
	outer := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(interceptors.ContextWithClaims(ctx, &interceptors.BasicClaims{UserID: 5, TeamID: 3}), req)
	}
	reject := func(ctx context.Context, _ any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		// A claim added here is never seen: the handler context was not published.
		return nil, status.Error(codes.Unauthenticated, "no")
	}
	_, err := chainUnary(context.Background(), func(context.Context, any) (any, error) { t.Fatal("handler ran"); return nil, nil },
		outer, calls.Unary, reject, calls.Unary)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v", status.Code(err))
	}
	if line := lines.last(t); line["user_id"] != uint(5) || line["team_id"] != uint(3) {
		t.Errorf("line user_id %v team_id %v, want the owner context's 5 3", line["user_id"], line["team_id"])
	}
	for _, ev := range evs.snapshot() {
		if c, ok := ev.(*grpcevents.RequestCompleted); ok && (c.UserID != 5 || c.TeamID != 3) {
			t.Errorf("completed UserID %d TeamID %d, want 5 3", c.UserID, c.TeamID)
		}
	}
}

// A handler an interceptor starts only after the call ended publishes its
// context too late to be read: no data race under -race, the call keeps
// its one terminal sequence, and the late handler still runs.
func TestCallLifecycle_HandlerContextPublishedAfterTheEndIsUnused(t *testing.T) {
	const n = 100
	evs := &callEvents{}
	calls := interceptors.CallLifecycle(interceptors.WithEventDispatcher(evs.dispatch), interceptors.WithRequestLine(), interceptors.WithLogger(newBoundLogger()))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		ended := make(chan struct{})
		ran := make(chan struct{})
		late := func(next func(context.Context) error, ctx context.Context) error {
			go func() {
				<-ended
				_ = next(interceptors.ContextWithClaims(ctx, &interceptors.BasicClaims{UserID: 9}))
			}()
			return status.Error(codes.DeadlineExceeded, "timed out")
		}
		wg.Add(1)
		go func(kind string) {
			defer wg.Done()
			_ = runChain(kind, calls, late, func(context.Context) error { close(ran); return nil })
			close(ended)
			<-ran
		}([]string{"unary", "stream"}[i%2])
	}
	wg.Wait()
	kinds, _, _ := evs.snapshot()
	var completed int
	for _, k := range kinds {
		if k == "completed" {
			completed++
		}
	}
	if completed != n {
		t.Errorf("completed events = %d, want %d", completed, n)
	}
}

// An ExtraFields that panics loses only its own fields: the request line
// is still written through the configured logger, and the call keeps its
// terminal sequence.
func TestCallLifecycle_PanickingExtraFieldsLosesOnlyItsFields(t *testing.T) {
	for _, kind := range []string{"unary", "stream"} {
		t.Run(kind, func(t *testing.T) {
			lines := newBoundLogger()
			evs := &callEvents{}
			calls := interceptors.CallLifecycle(interceptors.WithLogger(lines), interceptors.WithRequestLine(), interceptors.WithEventDispatcher(evs.dispatch),
				interceptors.WithExtraFields(func(context.Context) []any { panic("extra fields broke") }))
			err := runChain(kind, calls, passThrough, func(context.Context) error { return status.Error(codes.Internal, "broke") })
			if status.Code(err) != codes.Internal {
				t.Fatalf("code = %v", status.Code(err))
			}
			if line := lines.last(t); line["code"] != "Internal" {
				t.Errorf("request line %v, want the Internal line", line)
			}
			kinds, _, _ := evs.snapshot()
			if got := terminal(kinds); !equalKinds(got, []string{"started", "failed", "completed"}) {
				t.Errorf("lifecycle events = %v", got)
			}
		})
	}
}
