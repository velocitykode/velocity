package interceptors_test

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/trace"
)

// boundLine is one line a boundLogger recorded, bound pairs first.
type boundLine map[string]any

type boundSink struct {
	mu    sync.Mutex
	lines []boundLine
}

// boundLogger records the fields of every line, the pairs With bound
// first.
type boundLogger struct {
	sink  *boundSink
	bound []any
}

func newBoundLogger() boundLogger { return boundLogger{sink: &boundSink{}} }

func (l boundLogger) record(kvs []any) {
	all := append(append([]any(nil), l.bound...), kvs...)
	line := boundLine{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			line[k] = all[i+1]
		}
	}
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	l.sink.lines = append(l.sink.lines, line)
}

func (l boundLogger) Debug(_ string, kvs ...any) { l.record(kvs) }
func (l boundLogger) Info(_ string, kvs ...any)  { l.record(kvs) }
func (l boundLogger) Warn(_ string, kvs ...any)  { l.record(kvs) }
func (l boundLogger) Error(_ string, kvs ...any) { l.record(kvs) }
func (l boundLogger) Fatal(_ string, kvs ...any) { l.record(kvs) }

func (l boundLogger) With(kvs ...any) contract.Logger {
	return boundLogger{sink: l.sink, bound: append(append([]any(nil), l.bound...), kvs...)}
}

func (l boundLogger) last(t *testing.T) boundLine {
	t.Helper()
	l.sink.mu.Lock()
	defer l.sink.mu.Unlock()
	if len(l.sink.lines) == 0 {
		t.Fatal("nothing logged")
	}
	return l.sink.lines[len(l.sink.lines)-1]
}

// The line the call lifecycle interceptor writes for a call carries the call's
// request id, trace id and span id, unary and stream alike.
func TestCallLifecycle_LineCarriesTheCallCorrelation(t *testing.T) {
	traceparent := "00-" + callerTrace + "-" + callerSpan + "-01"
	for name, call := range map[string]func(pair interceptors.InterceptorPair) (trace, span, request string){
		"unary": func(pair interceptors.InterceptorPair) (string, string, string) {
			var tr, sp, rq string
			handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
				tr, sp, rq = trace.GetTraceID(ctx), trace.GetSpanID(ctx), trace.GetRequestID(ctx)
				return "ok", nil
			}
			if _, err := pair.Unary(incoming("traceparent", traceparent), nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
				t.Fatalf("unary: %v", err)
			}
			return tr, sp, rq
		},
		"stream": func(pair interceptors.InterceptorPair) (string, string, string) {
			var tr, sp, rq string
			handler := func(_ interface{}, ss grpc.ServerStream) error {
				ctx := ss.Context()
				tr, sp, rq = trace.GetTraceID(ctx), trace.GetSpanID(ctx), trace.GetRequestID(ctx)
				return nil
			}
			stream := &mockServerStream{ctx: incoming("traceparent", traceparent)}
			if err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/Stream"), handler); err != nil {
				t.Fatalf("stream: %v", err)
			}
			return tr, sp, rq
		},
	} {
		t.Run(name, func(t *testing.T) {
			logger := newBoundLogger()
			traceID, spanID, requestID := call(interceptors.CallLifecycle(interceptors.WithRequestLine(), interceptors.WithLogger(logger)))
			if traceID != callerTrace || spanID == "" || requestID == "" {
				t.Fatalf("handler ran with trace %q span %q request %q", traceID, spanID, requestID)
			}
			line := logger.last(t)
			for key, want := range map[string]string{"trace_id": traceID, "span_id": spanID, "request_id": requestID} {
				if got := line[key]; got != want {
					t.Errorf("%s = %v, want %q (%v)", key, got, want, line)
				}
			}
		})
	}
}

// The panic line of a recovered call carries the call's ids: its request
// id and its own span under the incoming trace.
func TestCallLifecycle_LineCarriesTheCallTrace(t *testing.T) {
	logger := newBoundLogger()
	pair := interceptors.CallLifecycle(interceptors.WithLogger(logger), interceptors.WithStackTrace(false))
	ctx := trace.WithRequestID(trace.WithTrace(context.Background(), callerTrace, callerSpan), "req-9")
	var callSpan string
	handler := func(ctx context.Context, _ interface{}) (interface{}, error) {
		callSpan = trace.GetSpanID(ctx)
		panic("boom")
	}

	_, _ = pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)

	if callSpan == callerSpan {
		t.Fatalf("the call ran under the caller's span %q, want a span of its own", callSpan)
	}
	line := logger.last(t)
	for key, want := range map[string]string{"trace_id": callerTrace, "span_id": callSpan, "request_id": "req-9"} {
		if got := line[key]; got != want {
			t.Errorf("%s = %v, want %q (%v)", key, got, want, line)
		}
	}
}
