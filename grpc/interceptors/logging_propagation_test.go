package interceptors_test

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
)

// fieldLogger records the key-value fields of every line logged through it.
type fieldLogger struct {
	mu    sync.Mutex
	lines []map[string]any
}

func (l *fieldLogger) record(kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fields := map[string]any{}
	for i := 0; i+1 < len(kvs); i += 2 {
		if k, ok := kvs[i].(string); ok {
			fields[k] = kvs[i+1]
		}
	}
	l.lines = append(l.lines, fields)
}

func (l *fieldLogger) Debug(_ string, kvs ...any) { l.record(kvs) }
func (l *fieldLogger) Info(_ string, kvs ...any)  { l.record(kvs) }
func (l *fieldLogger) Warn(_ string, kvs ...any)  { l.record(kvs) }
func (l *fieldLogger) Error(_ string, kvs ...any) { l.record(kvs) }
func (l *fieldLogger) Fatal(_ string, kvs ...any) { l.record(kvs) }

func (l *fieldLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }

func (l *fieldLogger) lastRequestID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	id, _ := l.lines[len(l.lines)-1]["request_id"].(string)
	return id
}

const (
	callerTrace = "4bf92f3577b34da6a3ce929d0e0e4736"
	callerSpan  = "00f067aa0ba902b7"
)

var generatedRequestID = regexp.MustCompile(`^[0-9a-f]{20}$`)

func incoming(pairs ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

// TestLoggingUnary_ContinuesTraceparentFromMetadata pins the server half of
// the gRPC edge: a valid traceparent in the incoming metadata is continued
// with a new span whose parent is the caller's span.
func TestLoggingUnary_ContinuesTraceparentFromMetadata(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))

	ctx := incoming("traceparent", "00-"+callerTrace+"-"+callerSpan+"-01")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil }
	if _, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var started *grpcevents.RequestStarted
	var completed *grpcevents.RequestCompleted
	for _, ev := range collector.snapshot() {
		switch e := ev.(type) {
		case *grpcevents.RequestStarted:
			started = e
		case *grpcevents.RequestCompleted:
			completed = e
		}
	}
	if started == nil || completed == nil {
		t.Fatalf("missing events: started=%v completed=%v", started != nil, completed != nil)
	}
	for name, got := range map[string][3]string{
		"RequestStarted":   {started.TraceID, started.SpanID, started.ParentID},
		"RequestCompleted": {completed.TraceID, completed.SpanID, completed.ParentID},
	} {
		if got[0] != callerTrace {
			t.Errorf("%s.TraceID = %q, want the caller's %q", name, got[0], callerTrace)
		}
		if got[1] == "" || got[1] == callerSpan {
			t.Errorf("%s.SpanID = %q, want a new span (caller span %q)", name, got[1], callerSpan)
		}
		if got[2] != callerSpan {
			t.Errorf("%s.ParentID = %q, want the caller's span %q", name, got[2], callerSpan)
		}
	}
}

// TestLoggingStream_ContinuesTraceparentFromMetadata is the stream variant.
func TestLoggingStream_ContinuesTraceparentFromMetadata(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))

	stream := &mockServerStream{ctx: incoming("traceparent", "00-"+callerTrace+"-"+callerSpan+"-00")}
	handler := func(srv interface{}, ss grpc.ServerStream) error { return nil }
	if err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/Stream"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var started *grpcevents.StreamStarted
	for _, ev := range collector.snapshot() {
		if e, ok := ev.(*grpcevents.StreamStarted); ok {
			started = e
		}
	}
	if started == nil {
		t.Fatal("StreamStarted not dispatched")
	}
	if started.TraceID != callerTrace || started.ParentID != callerSpan || started.SpanID == callerSpan || started.SpanID == "" {
		t.Errorf("StreamStarted trace=%q span=%q parent=%q, want trace %q with a new span under %q",
			started.TraceID, started.SpanID, started.ParentID, callerTrace, callerSpan)
	}
}

// TestLoggingUnary_MalformedTraceparentStartsRoot covers hostile or broken
// carriers: each is ignored and the call starts a root span.
func TestLoggingUnary_MalformedTraceparentStartsRoot(t *testing.T) {
	for _, header := range []string{
		"garbage",
		"00-" + callerTrace + "-" + callerSpan, // no flags
		"00-" + strings.ToUpper(callerTrace) + "-" + callerSpan + "-01",             // uppercase hex
		"00-00000000000000000000000000000000-" + callerSpan + "-01",                 // zero trace id
		"00-" + callerTrace + "-0000000000000000-01",                                // zero parent id
		"ff-" + callerTrace + "-" + callerSpan + "-01",                              // forbidden version
		"00-" + callerTrace + "-" + callerSpan + "-01-extra",                        // version 00 is exactly 55 bytes
		"00-" + callerTrace + "-" + callerSpan + "-0g",                              // non-hex flags
		"01-" + callerTrace + "-" + callerSpan + "-01-" + strings.Repeat("x", 1024), // oversized
	} {
		collector := &eventCollector{}
		pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))
		handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil }
		if _, err := pair.Unary(incoming("traceparent", header), nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, ev := range collector.snapshot() {
			if e, ok := ev.(*grpcevents.RequestStarted); ok {
				if e.TraceID == "" || e.TraceID == callerTrace || e.ParentID != "" {
					t.Errorf("traceparent %q: got trace=%q parent=%q, want a root span", header, e.TraceID, e.ParentID)
				}
			}
		}
	}

	// A repeated carrier is ambiguous, even when each value is valid.
	valid := "00-" + callerTrace + "-" + callerSpan + "-01"
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))
	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil }
	if _, err := pair.Unary(incoming("traceparent", valid, "traceparent", valid), nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, ev := range collector.snapshot() {
		if e, ok := ev.(*grpcevents.RequestStarted); ok && (e.TraceID == callerTrace || e.ParentID != "") {
			t.Errorf("repeated traceparent: got trace=%q parent=%q, want a root span", e.TraceID, e.ParentID)
		}
	}
}

// TestLoggingUnary_KeepsRequestIDFromMetadata pins that the x-request-id the
// caller sent is the request id of the call on the server.
func TestLoggingUnary_KeepsRequestIDFromMetadata(t *testing.T) {
	logger := &fieldLogger{}
	pair := interceptors.Logging(interceptors.WithLoggingLogger(logger))
	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil }
	if _, err := pair.Unary(incoming("x-request-id", "gateway-7f3a"), nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := logger.lastRequestID(); got != "gateway-7f3a" {
		t.Errorf("logged request_id = %q, want the caller's %q", got, "gateway-7f3a")
	}
}

// TestLoggingUnary_GeneratesRequestIDWhenAbsentOrInvalid pins that every
// call has a request id: absent or unusable inbound values are replaced by
// one from the framework's generator.
func TestLoggingUnary_GeneratesRequestIDWhenAbsentOrInvalid(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"absent":    context.Background(),
		"space":     incoming("x-request-id", "has space"),
		"quote":     incoming("x-request-id", `a"b`),
		"oversized": incoming("x-request-id", strings.Repeat("a", 129)),
		"repeated":  incoming("x-request-id", "one", "x-request-id", "two"),
	} {
		logger := &fieldLogger{}
		pair := interceptors.Logging(interceptors.WithLoggingLogger(logger))
		handler := func(ctx context.Context, req interface{}) (interface{}, error) { return "ok", nil }
		if _, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler); err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if got := logger.lastRequestID(); !generatedRequestID.MatchString(got) {
			t.Errorf("%s: logged request_id = %q, want a generated 20-hex id", name, got)
		}
	}
}
