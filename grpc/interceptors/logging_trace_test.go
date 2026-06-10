package interceptors_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	grpcpkg "github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/trace"
)

type eventCollector struct {
	mu     sync.Mutex
	events []interface{}
	ctxs   []context.Context
}

func (c *eventCollector) dispatch(ctx context.Context, event any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	c.ctxs = append(c.ctxs, ctx)
	return nil
}

func (c *eventCollector) snapshot() []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]interface{}, len(c.events))
	copy(out, c.events)
	return out
}

func (c *eventCollector) contexts() []context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]context.Context, len(c.ctxs))
	copy(out, c.ctxs)
	return out
}

func TestLoggingUnary_TraceMintedWhenNoIncomingTrace(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		if trace.GetTraceID(ctx) == "" {
			t.Error("handler ctx missing trace id")
		}
		return "ok", nil
	}

	_, err := pair.Unary(context.Background(), nil, mockUnaryServerInfo("/test.Service/Method"), handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var sawStarted, sawCompleted bool
	for _, ev := range collector.snapshot() {
		switch e := ev.(type) {
		case *grpcevents.RequestStarted:
			sawStarted = true
			if e.TraceID == "" || e.SpanID == "" {
				t.Errorf("RequestStarted: empty trace ids: %+v", e)
			}
			if e.ParentID != "" {
				t.Errorf("RequestStarted: parent should be empty for fresh trace, got %q", e.ParentID)
			}
		case *grpcevents.RequestCompleted:
			sawCompleted = true
			if e.TraceID == "" || e.SpanID == "" {
				t.Errorf("RequestCompleted: empty trace ids: %+v", e)
			}
		}
	}
	if !sawStarted || !sawCompleted {
		t.Errorf("missing events: started=%v completed=%v", sawStarted, sawCompleted)
	}

	// The dispatcher must receive the request ctx, not a detached one: each
	// dispatched ctx carries the trace minted for this request.
	for i, ctx := range collector.contexts() {
		if trace.GetTraceID(ctx) == "" {
			t.Errorf("dispatcher ctx %d missing trace id: request ctx not passed through", i)
		}
	}
}

func TestLoggingUnary_TraceExtendedWhenIncomingTracePresent(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))

	upstreamTrace := "trace12345678901234567890123456"
	upstreamSpan := "span1234567890ab"
	ctx := trace.WithTrace(context.Background(), upstreamTrace, upstreamSpan)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		if got := trace.GetTraceID(ctx); got != upstreamTrace {
			t.Errorf("handler trace id: got %q want %q", got, upstreamTrace)
		}
		if got := trace.GetParentID(ctx); got != upstreamSpan {
			t.Errorf("handler parent id: got %q want %q", got, upstreamSpan)
		}
		return "ok", nil
	}

	_, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, ev := range collector.snapshot() {
		switch e := ev.(type) {
		case *grpcevents.RequestStarted:
			if e.TraceID != upstreamTrace {
				t.Errorf("RequestStarted TraceID: got %q want %q", e.TraceID, upstreamTrace)
			}
			if e.ParentID != upstreamSpan {
				t.Errorf("RequestStarted ParentID: got %q want %q", e.ParentID, upstreamSpan)
			}
			if e.SpanID == "" || e.SpanID == upstreamSpan {
				t.Errorf("RequestStarted SpanID should be a fresh child span, got %q", e.SpanID)
			}
		case *grpcevents.RequestCompleted:
			if e.TraceID != upstreamTrace {
				t.Errorf("RequestCompleted TraceID: got %q want %q", e.TraceID, upstreamTrace)
			}
			if e.ParentID != upstreamSpan {
				t.Errorf("RequestCompleted ParentID: got %q want %q", e.ParentID, upstreamSpan)
			}
		}
	}
}

func TestLoggingStream_TracePropagatedThroughEventDispatcher(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Logging(interceptors.WithEventDispatcher(collector.dispatch))

	upstreamTrace := "stream1234567890abcdef1234567890"
	upstreamSpan := "stsp1234567890ab"
	ctx := trace.WithTrace(context.Background(), upstreamTrace, upstreamSpan)
	stream := &mockServerStream{ctx: ctx}

	var handlerSawTrace, handlerSawParent string
	handler := func(srv interface{}, ss grpc.ServerStream) error {
		handlerSawTrace = trace.GetTraceID(ss.Context())
		handlerSawParent = trace.GetParentID(ss.Context())
		return nil
	}

	if err := pair.Stream(nil, stream, mockStreamServerInfo("/test.Service/StreamMethod"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if handlerSawTrace != upstreamTrace {
		t.Errorf("handler stream ctx trace: got %q want %q", handlerSawTrace, upstreamTrace)
	}
	if handlerSawParent != upstreamSpan {
		t.Errorf("handler stream ctx parent: got %q want %q", handlerSawParent, upstreamSpan)
	}

	var sawStarted, sawCompleted bool
	for _, ev := range collector.snapshot() {
		switch e := ev.(type) {
		case *grpcevents.StreamStarted:
			sawStarted = true
			if e.TraceID != upstreamTrace || e.ParentID != upstreamSpan {
				t.Errorf("StreamStarted trace: got trace=%q parent=%q want trace=%q parent=%q",
					e.TraceID, e.ParentID, upstreamTrace, upstreamSpan)
			}
		case *grpcevents.StreamCompleted:
			sawCompleted = true
			if e.TraceID != upstreamTrace || e.ParentID != upstreamSpan {
				t.Errorf("StreamCompleted trace: got trace=%q parent=%q want trace=%q parent=%q",
					e.TraceID, e.ParentID, upstreamTrace, upstreamSpan)
			}
		}
	}
	if !sawStarted || !sawCompleted {
		t.Errorf("missing stream events: started=%v completed=%v", sawStarted, sawCompleted)
	}
}

func TestRecovery_PanicRecoveredEventCarriesTrace(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Recovery(interceptors.WithRecoveryEventDispatcher(collector.dispatch))

	traceID := "trace12345678901234567890123456"
	spanID := "span1234567890ab"
	ctx := trace.WithTrace(context.Background(), traceID, spanID)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("boom")
	}

	_, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Boom"), handler)
	if err == nil {
		t.Fatal("expected error after panic")
	}

	var seen *grpcevents.PanicRecovered
	for _, ev := range collector.snapshot() {
		if pe, ok := ev.(*grpcevents.PanicRecovered); ok {
			seen = pe
		}
	}
	if seen == nil {
		t.Fatal("PanicRecovered event not dispatched")
	}
	if seen.TraceID != traceID {
		t.Errorf("PanicRecovered TraceID: got %q want %q", seen.TraceID, traceID)
	}
	if seen.SpanID != spanID {
		t.Errorf("PanicRecovered SpanID: got %q want %q", seen.SpanID, spanID)
	}
	if seen.Method != "/test.Service/Boom" {
		t.Errorf("PanicRecovered Method: got %q", seen.Method)
	}
	if seen.Panic == nil {
		t.Error("PanicRecovered Panic value missing")
	}
}

type traceTestValidator struct{}

func (traceTestValidator) ValidateToken(ctx context.Context, token string) (grpcpkg.Claims, error) {
	return nil, errors.New("nope")
}

func TestAuth_AuthFailedEventCarriesTrace(t *testing.T) {
	collector := &eventCollector{}
	pair := interceptors.Auth(traceTestValidator{}, interceptors.WithAuthEventDispatcher(collector.dispatch))

	traceID := "auth123456789012345678901234abcd"
	spanID := "auth567890123456"
	md := metadata.New(map[string]string{"authorization": "Bearer abcdefghijkl"})
	ctx := metadata.NewIncomingContext(trace.WithTrace(context.Background(), traceID, spanID), md)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return nil, nil }
	if _, err := pair.Unary(ctx, nil, mockUnaryServerInfo("/test.Service/Method"), handler); err == nil {
		t.Fatal("expected auth error")
	}

	var seen *grpcevents.AuthFailed
	for _, ev := range collector.snapshot() {
		if af, ok := ev.(*grpcevents.AuthFailed); ok {
			seen = af
		}
	}
	if seen == nil {
		t.Fatal("AuthFailed event not dispatched")
	}
	if seen.TraceID != traceID {
		t.Errorf("AuthFailed TraceID: got %q want %q", seen.TraceID, traceID)
	}
	if seen.SpanID != spanID {
		t.Errorf("AuthFailed SpanID: got %q want %q", seen.SpanID, spanID)
	}
	if seen.Token == "" {
		t.Error("AuthFailed Token should be masked, not empty for 12-char input")
	}
	if seen.Token == "abcdefghijkl" {
		t.Error("AuthFailed Token leaked raw token")
	}
}

// recordingStream tracks which forwarded methods were called on the inner
// stream so the test can confirm tracedServerStream forwards each one
// rather than relying on embedding.
type recordingStream struct {
	ctx          context.Context
	sendMsgCalls int
	recvMsgCalls int
	setHdrCalls  int
	sendHdrCalls int
	setTrlrCalls int
}

func (r *recordingStream) Context() context.Context        { return r.ctx }
func (r *recordingStream) SendMsg(m interface{}) error     { r.sendMsgCalls++; return nil }
func (r *recordingStream) RecvMsg(m interface{}) error     { r.recvMsgCalls++; return nil }
func (r *recordingStream) SetHeader(md metadata.MD) error  { r.setHdrCalls++; return nil }
func (r *recordingStream) SendHeader(md metadata.MD) error { r.sendHdrCalls++; return nil }
func (r *recordingStream) SetTrailer(md metadata.MD)       { r.setTrlrCalls++ }

func TestLoggingStream_ForwardsAllServerStreamMethods(t *testing.T) {
	pair := interceptors.Logging(interceptors.WithEventDispatcher(func(context.Context, any) error { return nil }))

	rec := &recordingStream{ctx: context.Background()}

	handler := func(srv interface{}, ss grpc.ServerStream) error {
		if err := ss.SendMsg(nil); err != nil {
			return err
		}
		if err := ss.RecvMsg(nil); err != nil {
			return err
		}
		if err := ss.SetHeader(metadata.MD{}); err != nil {
			return err
		}
		if err := ss.SendHeader(metadata.MD{}); err != nil {
			return err
		}
		ss.SetTrailer(metadata.MD{})
		return nil
	}

	if err := pair.Stream(nil, rec, mockStreamServerInfo("/test.Service/StreamMethod"), handler); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.sendMsgCalls != 1 || rec.recvMsgCalls != 1 || rec.setHdrCalls != 1 || rec.sendHdrCalls != 1 || rec.setTrlrCalls != 1 {
		t.Errorf("forwarding miss: sendMsg=%d recvMsg=%d setHdr=%d sendHdr=%d setTrlr=%d",
			rec.sendMsgCalls, rec.recvMsgCalls, rec.setHdrCalls, rec.sendHdrCalls, rec.setTrlrCalls)
	}
}
