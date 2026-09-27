package cache

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/trace"
)

// TestCacheEvents_AreChildrenOfCallerSpan pins that each cache operation
// event is its own span: the caller's trace id, a span id of its own and the
// caller's span as ParentID.
func TestCacheEvents_AreChildrenOfCallerSpan(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	ctx := trace.WithTrace(context.Background(), callerTrace, callerSpan)

	if err := manager.PutWithContext(ctx, "span:key", "value", time.Minute); err != nil {
		t.Fatalf("PutWithContext: %v", err)
	}
	_, _ = manager.GetWithContext(ctx, "span:key")
	_, _ = manager.GetWithContext(ctx, "span:absent")

	check := func(name, traceID, spanID, parentID string) {
		t.Helper()
		if traceID != callerTrace {
			t.Errorf("%s.TraceID = %q, want the caller's %q", name, traceID, callerTrace)
		}
		if spanID == "" || spanID == callerSpan {
			t.Errorf("%s.SpanID = %q, want its own span (caller span %q)", name, spanID, callerSpan)
		}
		if parentID != callerSpan {
			t.Errorf("%s.ParentID = %q, want the caller's span %q", name, parentID, callerSpan)
		}
	}

	var sawWritten, sawHit, sawMiss bool
	for _, ev := range snapshotEvents(collector) {
		switch e := ev.(type) {
		case *CacheWritten:
			sawWritten = true
			check("CacheWritten", e.TraceID, e.SpanID, e.ParentID)
		case *CacheHit:
			sawHit = true
			check("CacheHit", e.TraceID, e.SpanID, e.ParentID)
		case *CacheMiss:
			sawMiss = true
			check("CacheMiss", e.TraceID, e.SpanID, e.ParentID)
		}
	}
	if !sawWritten || !sawHit || !sawMiss {
		t.Fatalf("missing events: written=%v hit=%v miss=%v", sawWritten, sawHit, sawMiss)
	}
}

// TestCacheEvents_WithoutTraceAreRootSpans states what an operation with no
// enclosing span records: a root span of its own (fresh trace id, fresh span
// id, no parent), so every event still names one span.
func TestCacheEvents_WithoutTraceAreRootSpans(t *testing.T) {
	collector := newTestEventCollector()
	manager := newTestManager(collector)

	_, _ = manager.GetWithContext(context.Background(), "root:absent")

	var miss *CacheMiss
	for _, ev := range snapshotEvents(collector) {
		if e, ok := ev.(*CacheMiss); ok {
			miss = e
		}
	}
	if miss == nil {
		t.Fatal("CacheMiss not dispatched")
	}
	if miss.TraceID == "" || miss.SpanID == "" || miss.ParentID != "" {
		t.Errorf("want a root span, got trace=%q span=%q parent=%q", miss.TraceID, miss.SpanID, miss.ParentID)
	}
}

// snapshotEvents copies the collected events under the collector's lock.
func snapshotEvents(c *testEventCollector) []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]interface{}(nil), c.events...)
}
