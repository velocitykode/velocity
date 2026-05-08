package trace

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateTraceID(t *testing.T) {
	id := GenerateTraceID()

	// Should be 32 hex characters (16 bytes * 2)
	if len(id) != 32 {
		t.Errorf("Expected trace ID length 32, got %d", len(id))
	}

	// Should only contain hex characters
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("Trace ID contains non-hex character: %c", c)
		}
	}

	// Each call should generate a unique ID
	id2 := GenerateTraceID()
	if id == id2 {
		t.Error("Expected unique trace IDs")
	}
}

func TestGenerateSpanID(t *testing.T) {
	id := GenerateSpanID()

	// Should be 16 hex characters (8 bytes * 2)
	if len(id) != 16 {
		t.Errorf("Expected span ID length 16, got %d", len(id))
	}

	// Should only contain hex characters
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("Span ID contains non-hex character: %c", c)
		}
	}

	// Each call should generate a unique ID
	id2 := GenerateSpanID()
	if id == id2 {
		t.Error("Expected unique span IDs")
	}
}

func TestWithTraceAndGetters(t *testing.T) {
	ctx := context.Background()
	traceID := "abc123def456789012345678901234ab"
	spanID := "span123456789012"

	ctx = WithTrace(ctx, traceID, spanID)

	gotTraceID := GetTraceID(ctx)
	if gotTraceID != traceID {
		t.Errorf("Expected trace ID %s, got %s", traceID, gotTraceID)
	}

	gotSpanID := GetSpanID(ctx)
	if gotSpanID != spanID {
		t.Errorf("Expected span ID %s, got %s", spanID, gotSpanID)
	}
}

func TestWithSpan_SetsParentID(t *testing.T) {
	ctx := context.Background()
	traceID := "abc123def456789012345678901234ab"
	parentSpan := "parent12345678"
	childSpan := "child123456789"

	// Set initial trace with parent span
	ctx = WithTrace(ctx, traceID, parentSpan)

	// Create child span
	ctx = WithSpan(ctx, childSpan)

	// Child span should be current
	if gotSpan := GetSpanID(ctx); gotSpan != childSpan {
		t.Errorf("Expected span ID %s, got %s", childSpan, gotSpan)
	}

	// Parent span should be preserved
	if gotParent := GetParentID(ctx); gotParent != parentSpan {
		t.Errorf("Expected parent ID %s, got %s", parentSpan, gotParent)
	}

	// Trace ID should be unchanged
	if gotTrace := GetTraceID(ctx); gotTrace != traceID {
		t.Errorf("Expected trace ID %s, got %s", traceID, gotTrace)
	}
}

func TestWithNewSpan(t *testing.T) {
	ctx := context.Background()
	traceID := GenerateTraceID()
	parentSpan := GenerateSpanID()

	ctx = WithTrace(ctx, traceID, parentSpan)

	newCtx, newSpanID := WithNewSpan(ctx)

	// New span ID should be different
	if newSpanID == parentSpan {
		t.Error("Expected new span ID to be different from parent")
	}

	// New span ID should be in context
	if gotSpan := GetSpanID(newCtx); gotSpan != newSpanID {
		t.Errorf("Expected span ID %s, got %s", newSpanID, gotSpan)
	}

	// Parent should be set
	if gotParent := GetParentID(newCtx); gotParent != parentSpan {
		t.Errorf("Expected parent ID %s, got %s", parentSpan, gotParent)
	}

	// Original context should be unchanged
	if gotSpan := GetSpanID(ctx); gotSpan != parentSpan {
		t.Errorf("Original context span should be %s, got %s", parentSpan, gotSpan)
	}
}

func TestGetTraceContext(t *testing.T) {
	ctx := context.Background()
	expectedTrace := "trace12345678901234567890123456"
	expectedSpan := "span1234567890ab"

	ctx = WithTrace(ctx, expectedTrace, expectedSpan)

	// Create child span to set parent
	childSpan := "child567890123456"
	ctx = WithSpan(ctx, childSpan)

	gotTrace, gotSpan, gotParent := GetTraceContext(ctx)

	if gotTrace != expectedTrace {
		t.Errorf("Expected trace ID %s, got %s", expectedTrace, gotTrace)
	}
	if gotSpan != childSpan {
		t.Errorf("Expected span ID %s, got %s", childSpan, gotSpan)
	}
	if gotParent != expectedSpan {
		t.Errorf("Expected parent ID %s, got %s", expectedSpan, gotParent)
	}
}

func TestStartTrace(t *testing.T) {
	ctx := context.Background()

	newCtx, traceID, spanID := StartTrace(ctx)

	// Should have generated valid IDs
	if len(traceID) != 32 {
		t.Errorf("Expected trace ID length 32, got %d", len(traceID))
	}
	if len(spanID) != 16 {
		t.Errorf("Expected span ID length 16, got %d", len(spanID))
	}

	// IDs should be in context
	if got := GetTraceID(newCtx); got != traceID {
		t.Errorf("Expected trace ID %s in context, got %s", traceID, got)
	}
	if got := GetSpanID(newCtx); got != spanID {
		t.Errorf("Expected span ID %s in context, got %s", spanID, got)
	}
}

func TestContinueTrace_ExistingTrace(t *testing.T) {
	ctx := context.Background()
	originalTrace := GenerateTraceID()
	originalSpan := GenerateSpanID()

	ctx = WithTrace(ctx, originalTrace, originalSpan)

	newCtx, newSpanID := ContinueTrace(ctx)

	// Trace ID should be unchanged
	if got := GetTraceID(newCtx); got != originalTrace {
		t.Errorf("Expected trace ID %s, got %s", originalTrace, got)
	}

	// Span ID should be new
	if newSpanID == originalSpan {
		t.Error("Expected new span ID")
	}
	if got := GetSpanID(newCtx); got != newSpanID {
		t.Errorf("Expected span ID %s, got %s", newSpanID, got)
	}

	// Parent should be set to original span
	if got := GetParentID(newCtx); got != originalSpan {
		t.Errorf("Expected parent ID %s, got %s", originalSpan, got)
	}
}

func TestContinueTrace_NoExistingTrace(t *testing.T) {
	ctx := context.Background()

	newCtx, spanID := ContinueTrace(ctx)

	// Should have created a new trace
	traceID := GetTraceID(newCtx)
	if len(traceID) != 32 {
		t.Errorf("Expected new trace ID to be created, got %s", traceID)
	}

	if len(spanID) != 16 {
		t.Errorf("Expected span ID length 16, got %d", len(spanID))
	}
}

func TestGettersWithNilContext(t *testing.T) {
	// Should not panic with nil context. The nil-ctx path is the
	// subject under test — staticcheck SA1012 warnings are intentional here.
	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	if got := GetTraceID(nil); got != "" {
		t.Errorf("Expected empty string for nil context, got %s", got)
	}
	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	if got := GetSpanID(nil); got != "" {
		t.Errorf("Expected empty string for nil context, got %s", got)
	}
	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	if got := GetParentID(nil); got != "" {
		t.Errorf("Expected empty string for nil context, got %s", got)
	}
}

func TestGettersWithEmptyContext(t *testing.T) {
	ctx := context.Background()

	// Should return empty strings when not set
	if got := GetTraceID(ctx); got != "" {
		t.Errorf("Expected empty string, got %s", got)
	}
	if got := GetSpanID(ctx); got != "" {
		t.Errorf("Expected empty string, got %s", got)
	}
	if got := GetParentID(ctx); got != "" {
		t.Errorf("Expected empty string, got %s", got)
	}
}

func TestNestedSpans(t *testing.T) {
	ctx := context.Background()

	// Start a trace
	ctx, traceID, rootSpan := StartTrace(ctx)

	// Create first child
	ctx, child1 := WithNewSpan(ctx)
	if GetParentID(ctx) != rootSpan {
		t.Error("First child parent should be root span")
	}

	// Create second child (grandchild of root)
	ctx, child2 := WithNewSpan(ctx)
	if GetParentID(ctx) != child1 {
		t.Error("Second child parent should be first child")
	}

	// Trace ID should be unchanged throughout
	if GetTraceID(ctx) != traceID {
		t.Error("Trace ID should not change")
	}

	// All spans should be unique
	spans := []string{rootSpan, child1, child2}
	seen := make(map[string]bool)
	for _, span := range spans {
		if seen[span] {
			t.Error("Duplicate span ID found")
		}
		seen[span] = true
	}
}

func TestWithFullContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	traceID := "trace12345678901234567890123456"
	spanID := "span1234567890ab"
	parentID := "parent7890123456"

	ctx = WithFullContext(ctx, traceID, spanID, parentID)

	gotTrace, gotSpan, gotParent := GetTraceContext(ctx)
	if gotTrace != traceID {
		t.Errorf("trace: got %s, want %s", gotTrace, traceID)
	}
	if gotSpan != spanID {
		t.Errorf("span: got %s, want %s", gotSpan, spanID)
	}
	if gotParent != parentID {
		t.Errorf("parent: got %s, want %s", gotParent, parentID)
	}
}

func TestWithFullContext_EmptyStringsStoredVerbatim(t *testing.T) {
	ctx := WithTrace(context.Background(), "old-trace", "old-span")
	ctx = WithSpan(ctx, "old-child")

	ctx = WithFullContext(ctx, "", "", "")

	if got := GetTraceID(ctx); got != "" {
		t.Errorf("trace: got %s, want empty", got)
	}
	if got := GetSpanID(ctx); got != "" {
		t.Errorf("span: got %s, want empty", got)
	}
	if got := GetParentID(ctx); got != "" {
		t.Errorf("parent: got %s, want empty", got)
	}
}
