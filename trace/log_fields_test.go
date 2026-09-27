package trace

import (
	"context"
	"reflect"
	"testing"
)

func TestLogFields(t *testing.T) {
	full := WithRequestID(WithTrace(context.Background(), "t1", "s1"), "r1")
	tests := []struct {
		name string
		ctx  context.Context
		want []any
	}{
		{"all ids", full, []any{"request_id", "r1", "trace_id", "t1", "span_id", "s1"}},
		{"trace only", WithTrace(context.Background(), "t1", "s1"), []any{"trace_id", "t1", "span_id", "s1"}},
		{"none", context.Background(), []any{}},
		{"nil", nil, nil},
	}
	for _, tt := range tests {
		if got := LogFields(tt.ctx); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: LogFields = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// LogFields names the ids under the keys NewErrorContext's reports are
// logged with, and generates a lazy id the same way.
func TestLogFields_MatchNewErrorContext(t *testing.T) {
	ctx, _ := StartTraceLazy(context.Background())
	ctx, _ = WithLazyRequestID(ctx)

	fields := LogFields(ctx)
	ec := NewErrorContext(ctx)

	want := []any{"request_id", ec.RequestID, "trace_id", ec.TraceID, "span_id", ec.SpanID}
	if !reflect.DeepEqual(fields, want) {
		t.Errorf("LogFields = %v, want %v", fields, want)
	}
}
