// Package fixture — this file lives under testdata/ so the Go toolchain
// ignores it at build/test time. The skip checker scans it as if it were
// a real _test.go file. Every skip here is unguarded and MUST be flagged.
package fixture

import "testing"

func TestNakedSkip(t *testing.T) {
	t.Skip("TODO: broken")
	_ = 1
}

func TestSkipNowUnguarded(t *testing.T) {
	x := 1
	y := 2
	_ = x
	_ = y
	t.SkipNow()
}

func TestSkipfUnguarded(t *testing.T) {
	t.Skipf("TODO: %s", "investigate")
}

func TestSkipFarFromAnyConditional(t *testing.T) {
	// No conditional anywhere in this function — the old 8-line sed
	// window correctly flagged this. The AST checker must also flag it.
	a := 1
	b := 2
	c := 3
	d := 4
	e := 5
	f := 6
	g := 7
	h := 8
	i := 9
	j := 10
	_, _, _, _, _ = a, b, c, d, e
	_, _, _, _, _ = f, g, h, i, j
	t.Skip("still naked")
}
