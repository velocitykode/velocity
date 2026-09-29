package events

import (
	"context"
	"testing"
)

// BenchmarkDispatch_FailureEventBridge measures a failure event's trip
// through the failure-report bridge and its per-goroutine re-entry guard.
func BenchmarkDispatch_FailureEventBridge(b *testing.B) {
	d := NewDispatcher()
	d.SetFailureReporter(func(context.Context, interface{}, error) {})
	ev := &otherFailed{err: "boom"}
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_ = d.Dispatch(ctx, ev)
	}
}
