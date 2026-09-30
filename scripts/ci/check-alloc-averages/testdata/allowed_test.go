// Package fixture: nothing here may be flagged.
package fixture

import "testing"

func BenchmarkAllocs(b *testing.B) {
	_ = testing.AllocsPerRun(1, func() {})
	b.Run("sub", func(b *testing.B) {
		_ = testing.AllocsPerRun(1, func() {})
	})
}

func Benchmark(b *testing.B) {
	_ = testing.AllocsPerRun(1, func() {})
}

func Benchmark_Under(b *testing.B) {
	_ = testing.AllocsPerRun(1, func() {})
}

type other struct{ AllocsPerRun func() }

func TestOtherSelector(t *testing.T) {
	var o other
	o.AllocsPerRun = func() {}
	o.AllocsPerRun()
}
