package eventemit

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// errSweep is the failure the sweeps record.
var errSweep = errors.New("listener failed")

// runSwept runs call, the entry point that reaches the hostile code c. A
// panic must not reach the caller; a Block code is released once probe,
// run meanwhile on the test goroutine, returned.
func runSwept(t *testing.T, mode hostile.Mode, c *hostile.Code, call, probe func()) {
	t.Helper()
	if mode != hostile.Block {
		if p := hostile.Within(t, hostile.Deadline, call); p != nil {
			t.Fatalf("panic escaped: %v", p)
		}
		return
	}
	done := make(chan any, 1)
	go func() { done <- hostile.Within(t, hostile.Deadline, call) }() //safe-goroutine: Within recovers and hands the panic back through done
	select {
	case <-c.Entered():
	case <-time.After(hostile.Deadline):
		t.Fatal("the user code never ran")
	}
	if p := hostile.Within(t, hostile.Deadline, probe); p != nil {
		t.Fatalf("probe panicked: %v", p)
	}
	c.Release()
	if p := <-done; p != nil {
		t.Fatalf("panic escaped: %v", p)
	}
}

// An Emitter survives a dispatcher that panics, blocks or re-enters: the
// panic is recorded once, and no lock is held while the dispatcher runs,
// so another Emit, a Set or a Fail returns meanwhile.
func TestHostileDispatcher_Emitter(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			var e Emitter
			ev := namedEvent{name: "cache.hit"}
			other := hostile.NewDispatcher(nil)
			probe := func() {
				e.Emit(context.Background(), ev)
				e.Fail(context.Background(), errSweep, ev)
				_ = e.Installed()
			}
			c := hostile.New(t, mode, probe)
			d := hostile.NewDispatcher(c)
			e.Set(d.Dispatch)
			// While the dispatcher blocks, the probe emits through another
			// one, so it does not block on the same code.
			runSwept(t, mode, c, func() { e.Emit(context.Background(), ev) }, func() {
				e.Set(other.Dispatch)
				probe()
				e.Set(d.Dispatch)
			})

			// One failure: the panic, or the probe's Fail.
			if got := e.FailureCount(); got != 1 {
				t.Errorf("FailureCount = %d, want 1", got)
			}
		})
	}
}

// The failure policy survives a hook that panics, blocks or re-enters: the
// failure is counted before the hook runs, a panicking hook is counted as
// a failure of its own, and no lock is held while the hook runs, so
// another Record returns meanwhile and a hook recording again does not
// recurse.
func TestHostileHook_Failures(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			var f Failures
			logger := &recordingLogger{}
			ev := namedEvent{name: "cache.hit"}
			probe := func() { f.Record(context.Background(), logger, errSweep, ev) }
			c := hostile.New(t, mode, probe)
			var hooked atomic.Int32
			// Only the first hook call runs the code, so the probe's own
			// hook call does not block on it.
			f.SetHook(func(error, any) {
				if hooked.Add(1) == 1 {
					c.Run()
				}
			})
			runSwept(t, mode, c, probe, probe)

			// Two failures: the call's and the probe's, or, when the hook
			// panicked, the call's and the hook's own.
			if got := f.Count(); got != 2 {
				t.Errorf("Count = %d, want 2", got)
			}
			if got := hooked.Load(); got < 1 {
				t.Errorf("hook calls = %d, want at least 1", got)
			}
		})
	}
}

// The failure policy survives a logger that panics, blocks or re-enters on
// the failure line: the failure is still counted and handed to the hook
// (a failing diagnostic never skips the accounting), and no lock is held
// while the logger runs.
func TestHostileLogger_Failures(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			var f Failures
			var hooked atomic.Int32
			f.SetHook(func(error, any) { hooked.Add(1) })
			var logger *hostile.Logger
			record := func(name string) func() {
				return func() { f.Record(context.Background(), logger, errSweep, namedEvent{name: name}) }
			}
			c := hostile.New(t, mode, record("reentered"))
			logger = hostile.NewLogger(c, hostile.Warn)
			// Only the first failure of a name writes the line, so the probe,
			// failing under the same name, writes none and does not block.
			runSwept(t, mode, c, record("cache.hit"), record("cache.hit"))

			// The call's failure, plus the probe's or the re-entered one.
			want := uint64(2)
			if mode == hostile.Panic {
				want = 1
			}
			if got := f.Count(); got != want {
				t.Errorf("Count = %d, want %d", got, want)
			}
			if got := hooked.Load(); uint64(got) != want {
				t.Errorf("hook calls = %d, want %d", got, want)
			}
		})
	}
}
