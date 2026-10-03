package router

import "testing"

// BenchmarkRequestAdmission measures what admission adds to every
// request: one Admit and one Release on the router's request run. It
// allocates nothing (pinned in scripts/ci/check-zero-alloc-benchmarks.sh).
func BenchmarkRequestAdmission(b *testing.B) {
	r := New()
	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if !r.requests.Admit() {
				b.Fatal("admission refused")
			}
			r.requests.Release()
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if !r.requests.Admit() {
					b.Fatal("admission refused")
				}
				r.requests.Release()
			}
		})
	})
}
