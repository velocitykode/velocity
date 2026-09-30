package eventemit

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

type benchLogger struct{}

func (benchLogger) Debug(string, ...any)          {}
func (benchLogger) Info(string, ...any)           {}
func (benchLogger) Warn(string, ...any)           {}
func (benchLogger) Error(string, ...any)          {}
func (benchLogger) Fatal(string, ...any)          {}
func (l benchLogger) With(...any) contract.Logger { return l }

// BenchmarkFailuresRecord measures the failure path with a hook installed:
// the count, the (already logged) first-failure check and the hook call
// behind its per-goroutine re-entry guard.
func BenchmarkFailuresRecord(b *testing.B) {
	var f Failures
	f.SetHook(func(error, any) {})
	err := errors.New("boom")
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		f.Record(ctx, benchLogger{}, err, "bench.event")
	}
}

// BenchmarkEmitterEmit measures the success path of Emit: the dispatcher
// load and the contained call of a dispatcher that succeeds.
func BenchmarkEmitterEmit(b *testing.B) {
	var e Emitter
	e.Set(func(context.Context, any) error { return nil })
	ctx := context.Background()
	ev := &struct{}{}
	b.ReportAllocs()
	for b.Loop() {
		e.EmitBuilt(ctx, func() any { return ev })
	}
}

// BenchmarkEmitterEmitParallel is BenchmarkEmitterEmit from every P at
// once: the binding load is shared, read-only state.
func BenchmarkEmitterEmitParallel(b *testing.B) {
	var e Emitter
	e.Set(func(context.Context, any) error { return nil })
	ev := &struct{}{}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			e.EmitBuilt(ctx, func() any { return ev })
		}
	})
}

// BenchmarkEmitterInstalled measures the check a component makes before
// building an event.
func BenchmarkEmitterInstalled(b *testing.B) {
	var e Emitter
	e.Set(func(context.Context, any) error { return nil })
	b.ReportAllocs()
	for b.Loop() {
		if !e.Installed() {
			b.Fatal("not installed")
		}
	}
}

// BenchmarkEmitterEmitFailing measures Emit through a dispatcher whose
// failure the app's dispatch function already recorded: the failure path
// every emitter in an app takes.
func BenchmarkEmitterEmitFailing(b *testing.B) {
	var e Emitter
	var app Failures
	e.SetShared(app.Recording(failing, benchLogger{}), &app, nil)
	ctx := context.Background()
	ev := namedEvent{"bench.event"}
	b.ReportAllocs()
	for b.Loop() {
		e.EmitBuilt(ctx, func() any { return ev })
	}
}
