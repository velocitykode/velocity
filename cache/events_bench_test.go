package cache

import (
	"context"
	"testing"
)

// BenchmarkCacheHitEvent measures the CacheHit helper with and without an
// event dispatcher: with none, no event is built.
func BenchmarkCacheHitEvent(b *testing.B) {
	ctx := context.Background()
	b.Run("none", func(b *testing.B) {
		m := &Manager{}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.dispatchCacheHit(ctx, "k", "memory")
		}
	})
	b.Run("dispatcher", func(b *testing.B) {
		m := &Manager{}
		m.SetEventDispatcher(func(context.Context, any) error { return nil })
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m.dispatchCacheHit(ctx, "k", "memory")
		}
	})
}

// BenchmarkCacheHitEventParallel is BenchmarkCacheHitEvent run in
// parallel.
func BenchmarkCacheHitEventParallel(b *testing.B) {
	for _, withDisp := range []bool{false, true} {
		name := "none"
		if withDisp {
			name = "dispatcher"
		}
		b.Run(name, func(b *testing.B) {
			m := &Manager{}
			if withDisp {
				m.SetEventDispatcher(func(context.Context, any) error { return nil })
			}
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				ctx := context.Background()
				for pb.Next() {
					m.dispatchCacheHit(ctx, "k", "memory")
				}
			})
		})
	}
}
