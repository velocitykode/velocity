// Fixture for the skip checker. Every skip here is properly guarded and
// MUST NOT be flagged. Covers the common forms: env gate, runtime check,
// short-mode gate, err check, skip inside nested if, skip inside switch,
// skip inside select.
package fixture

import (
	"os"
	"runtime"
	"testing"
)

func TestGuardedByEnv(t *testing.T) {
	if os.Getenv("INTEGRATION") == "" {
		t.Skip("INTEGRATION not set")
	}
}

func TestGuardedByGOOS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix only")
	}
	// Plenty of setup after the guard — the sed window would miss this.
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
}

func TestGuardedByShort(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
}

func TestGuardedByErr(t *testing.T) {
	err := doSetup()
	if err != nil {
		t.Skipf("setup failed: %v", err)
	}
}

func TestLongGuardFarAboveSkip(t *testing.T) {
	// Guard is at the top of a long function — the 8-line sed window
	// would have been fooled here into reporting unguarded.
	if os.Getenv("CI") == "" {
		t.Skip("CI only")
	}
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
}

func TestGuardedBySwitch(t *testing.T) {
	switch runtime.GOOS {
	case "linux":
		t.Skip("linux-specific")
	}
}

func TestGuardedBySelect(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 1
	select {
	case <-ch:
		t.Skip("channel received")
	}
}

func TestGuardedByTableField(t *testing.T) {
	// The table-driven pattern: `if tt.skip { t.Skip(...) }`.
	tests := []struct {
		skip   bool
		reason string
	}{
		{skip: true, reason: "pending"},
	}
	for _, tt := range tests {
		if tt.skip {
			t.Skip(tt.reason)
		}
	}
}

func doSetup() error { return nil }
