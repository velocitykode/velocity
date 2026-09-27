package trace

import (
	"context"
	"testing"
	"time"
)

// NewErrorContext carries the request, trace and span ids of its context,
// generating lazy ones, and is stamped and ready for extra fields.
func TestNewErrorContext(t *testing.T) {
	ctx, lazyTrace := StartTraceLazy(context.Background())
	ctx, lazyID := WithLazyRequestID(ctx)
	before := time.Now()

	ec := NewErrorContext(ctx)

	traceID, spanID := lazyTrace.IDs()
	if ec.RequestID != lazyID.ID() || ec.TraceID != traceID || ec.SpanID != spanID {
		t.Errorf("ids = (%q, %q, %q), want the context's (%q, %q, %q)", ec.RequestID, ec.TraceID, ec.SpanID, lazyID.ID(), traceID, spanID)
	}
	if ec.RequestID == "" || ec.TraceID == "" || ec.SpanID == "" {
		t.Errorf("lazy ids not generated: %+v", ec)
	}
	if ec.Timestamp.Before(before) {
		t.Errorf("Timestamp = %v, want at or after %v", ec.Timestamp, before)
	}
	if ec.Extra == nil {
		t.Error("Extra is nil, want an empty map")
	}
}

// A context without ids yields none, a nil one included.
func TestNewErrorContext_NoIDs(t *testing.T) {
	for name, ctx := range map[string]context.Context{"background": context.Background(), "nil": nil} {
		ec := NewErrorContext(ctx)
		if ec.RequestID != "" || ec.TraceID != "" || ec.SpanID != "" {
			t.Errorf("%s: ids = (%q, %q, %q), want none", name, ec.RequestID, ec.TraceID, ec.SpanID)
		}
		if ec.Timestamp.IsZero() || ec.Extra == nil {
			t.Errorf("%s: not stamped or no Extra: %+v", name, ec)
		}
	}
}
