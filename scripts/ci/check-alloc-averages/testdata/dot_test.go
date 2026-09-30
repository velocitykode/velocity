// Package fixture: a dot import of testing is still testing.
package fixture

import . "testing"

func TestDot(t *T) {
	_ = AllocsPerRun(1, func() {})
}

func BenchmarkDot(b *B) {
	_ = AllocsPerRun(1, func() {})
}

type withField struct{ AllocsPerRun func() }

func TestDotSelector(t *T) {
	var w withField
	w.AllocsPerRun = func() {}
}
