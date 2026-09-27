package trace

import (
	"context"
	"testing"
)

func TestStartSpan_ContinuesValidParent(t *testing.T) {
	ctx := StartSpan(context.Background(), Parent{TraceID: w3cTrace, SpanID: w3cSpan})
	traceID, spanID, parentID := GetTraceContext(ctx)
	if traceID != w3cTrace {
		t.Errorf("trace id = %q, want the parent's %q", traceID, w3cTrace)
	}
	if !isW3CID(spanID) || len(spanID) != 16 || spanID == w3cSpan {
		t.Errorf("span id = %q, want a fresh 16-hex span", spanID)
	}
	if parentID != w3cSpan {
		t.Errorf("parent id = %q, want the caller's span %q", parentID, w3cSpan)
	}
}

func TestStartSpan_ZeroParentStartsRoot(t *testing.T) {
	ctx := StartSpan(context.Background(), Parent{})
	traceID, spanID, parentID := GetTraceContext(ctx)
	if len(traceID) != 32 || !isW3CID(traceID) || len(spanID) != 16 || !isW3CID(spanID) {
		t.Errorf("root span ids = %q/%q, want fresh W3C-shaped ids", traceID, spanID)
	}
	if parentID != "" {
		t.Errorf("root parent id = %q, want empty", parentID)
	}
}

// TestStartSpan_ReplacesStaleIDs pins that StartSpan never lets ids already
// in ctx leak into the new span: a root clears a stale parent, and a
// continued span takes the carrier's trace, not ctx's.
func TestStartSpan_ReplacesStaleIDs(t *testing.T) {
	stale := WithFullContext(context.Background(), "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331", "53995c3f42cd8ad8")

	root := StartSpan(stale, Parent{})
	if traceID, _, parentID := GetTraceContext(root); traceID == "0af7651916cd43dd8448eb211c80319c" || parentID != "" {
		t.Errorf("root kept stale ids: trace=%q parent=%q", traceID, parentID)
	}

	child := StartSpan(stale, Parent{TraceID: w3cTrace, SpanID: w3cSpan})
	if traceID, _, parentID := GetTraceContext(child); traceID != w3cTrace || parentID != w3cSpan {
		t.Errorf("continued span = trace %q parent %q, want trace %q parent %q", traceID, parentID, w3cTrace, w3cSpan)
	}
}

func TestStartSpan_NilContext(t *testing.T) {
	//lint:ignore SA1012 exercising the nil-context code path is the point of this test
	ctx := StartSpan(nil, Parent{TraceID: w3cTrace, SpanID: w3cSpan})
	if GetTraceID(ctx) != w3cTrace {
		t.Errorf("trace id = %q", GetTraceID(ctx))
	}
}

func TestChildSpanIDs(t *testing.T) {
	ctx := WithTrace(context.Background(), w3cTrace, w3cSpan)
	traceID, spanID, parentID := ChildSpanIDs(ctx)
	if traceID != w3cTrace || parentID != w3cSpan || spanID == "" || spanID == w3cSpan {
		t.Errorf("ChildSpanIDs = %q/%q/%q, want trace %q, a new span, parent %q", traceID, spanID, parentID, w3cTrace, w3cSpan)
	}
	if _, again, _ := ChildSpanIDs(ctx); again == spanID {
		t.Errorf("two operations share span %q", spanID)
	}
	// ctx itself is unchanged: the ids are returned, not stored.
	if GetSpanID(ctx) != w3cSpan {
		t.Errorf("ChildSpanIDs changed ctx's span to %q", GetSpanID(ctx))
	}

	traceID, spanID, parentID = ChildSpanIDs(context.Background())
	if traceID == "" || spanID == "" || parentID != "" {
		t.Errorf("untraced ChildSpanIDs = %q/%q/%q, want a root span", traceID, spanID, parentID)
	}
}

// TestChildSpanIDs_KeepsNonW3CTrace pins that in-process continuation does
// not validate id shape: an operation under a fallback-marker trace (minted
// while crypto/rand was unavailable) stays in that trace.
func TestChildSpanIDs_KeepsNonW3CTrace(t *testing.T) {
	marker := fallbackTraceID()
	ctx := WithTrace(context.Background(), marker, "span-marker")
	if traceID, _, parentID := ChildSpanIDs(ctx); traceID != marker || parentID != "span-marker" {
		t.Errorf("ChildSpanIDs = trace %q parent %q, want trace %q parent %q", traceID, parentID, marker, "span-marker")
	}
}

func TestContinueTrace_UsesTheRule(t *testing.T) {
	ctx, spanID := ContinueTrace(WithTrace(context.Background(), w3cTrace, w3cSpan))
	if GetTraceID(ctx) != w3cTrace || GetParentID(ctx) != w3cSpan || GetSpanID(ctx) != spanID || spanID == w3cSpan {
		t.Errorf("ContinueTrace: trace=%q span=%q parent=%q returned span=%q", GetTraceID(ctx), GetSpanID(ctx), GetParentID(ctx), spanID)
	}
}
