package events

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/eventemit"
)

// BenchmarkEmitRecordingDispatch measures a framework component's emit as
// an app wires it: the Emitter, the app's recording dispatch function and
// a DefaultDispatcher with one listener that succeeds.
func BenchmarkEmitRecordingDispatch(b *testing.B) {
	d := NewDispatcher()
	d.Listen("bench.event", &BaseListener{})
	var f eventemit.Failures
	var e eventemit.Emitter
	e.Set(f.Recording(d.Dispatch, nil))
	ctx := context.Background()
	ev := &BaseEvent{EventName: "bench.event"}
	b.ReportAllocs()
	for b.Loop() {
		e.Emit(ctx, ev)
	}
}
