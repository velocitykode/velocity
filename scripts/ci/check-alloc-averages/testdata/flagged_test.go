// Package fixture lives under testdata/ so the Go toolchain ignores it.
// Every AllocsPerRun reference outside a benchmark here MUST be flagged.
package fixture

import "testing"

func TestCompare(t *testing.T) {
	allocs := testing.AllocsPerRun(100, func() {})
	if allocs != 0 {
		t.Fail()
	}
}

func helper(f func()) float64 {
	return testing.AllocsPerRun(10, f)
}

var measure = testing.AllocsPerRun

func TestInClosure(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		_ = testing.AllocsPerRun(1, func() {})
	})
}

type suite struct{}

func (suite) BenchmarkMethod(b *testing.B) {
	_ = testing.AllocsPerRun(1, func() {})
}

func Benchmarkx(b *testing.B) {
	_ = testing.AllocsPerRun(1, func() {})
}
