package trace

import "testing"

// BenchmarkLazyTrace_IDs measures the first read of a request's lazy trace
// ids, the path a request that logs or dispatches pays once.
func BenchmarkLazyTrace_IDs(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		var l LazyTrace
		_, _ = l.IDs()
	}
}
