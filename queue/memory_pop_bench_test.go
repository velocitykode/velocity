package queue

import (
	"context"
	"testing"
)

type popBenchJob struct{}

func (popBenchJob) Handle() error { return nil }
func (popBenchJob) Failed(error)  {}
func (popBenchJob) JobID() string { return "pop-bench" }

// BenchmarkMemoryDriver_Pop measures the memory driver's per-job round
// trip for an in-process job (the common path: the wrapper keeps the live
// job and no factory runs), not an isolated pop: each iteration is a push
// then a pop, and for PopCtxReserved a push, a reserved pop and its ack.
func BenchmarkMemoryDriver_Pop(b *testing.B) {
	ctx := context.Background()
	const q = "bench-pop"
	b.Run("PopCtxWithTrace", func(b *testing.B) {
		d := NewMemoryDriver()
		b.Cleanup(func() { _ = d.Shutdown(ctx) })
		job := popBenchJob{}
		b.ReportAllocs()
		for b.Loop() {
			if err := d.PushCtx(ctx, job, q); err != nil {
				b.Fatal(err)
			}
			if j, _, err := d.PopCtxWithTrace(ctx, q); err != nil || j == nil {
				b.Fatal(j, err)
			}
		}
	})
	b.Run("PopCtxReserved", func(b *testing.B) {
		d := NewMemoryDriver()
		b.Cleanup(func() { _ = d.Shutdown(ctx) })
		job := popBenchJob{}
		b.ReportAllocs()
		for b.Loop() {
			if err := d.PushCtx(ctx, job, q); err != nil {
				b.Fatal(err)
			}
			j, token, _, err := d.PopCtxReserved(ctx, q)
			if err != nil || j == nil {
				b.Fatal(j, err)
			}
			if err := d.AckCtx(ctx, token); err != nil {
				b.Fatal(err)
			}
		}
	})
}
