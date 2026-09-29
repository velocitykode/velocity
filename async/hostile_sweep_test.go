package async

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// sweptHelpers runs fn through each helper and waits for the helper to
// finish with it (or, for a bounded helper, for its bound).
func sweptHelpers() map[string]func(fn func() int) {
	wait := func(start func(func())) func(fn func() int) {
		return func(fn func() int) {
			done := make(chan struct{})
			start(func() {
				defer close(done)
				fn()
			})
			<-done
		}
	}
	return map[string]func(fn func() int){
		"Run":             func(fn func() int) { _, _ = Run(fn).Get() },
		"RunWithTimeout":  func(fn func() int) { _, _ = RunWithTimeout(time.Second, fn).Get() },
		"RunWithContext":  func(fn func() int) { _, _ = RunWithContext(context.Background(), fn).Get() },
		"Go":              wait(func(f func()) { Go(f) }),
		"GoCtx":           wait(func(f func()) { GoCtx(context.Background(), func(context.Context) { f() }) }),
		"GoWithLogger":    wait(func(f func()) { GoWithLogger(nil, "swept", f) }),
		"All":             func(fn func() int) { _, _ = All(fn) },
		"AllN":            func(fn func() int) { _, _ = AllN(1, fn) },
		"Race":            func(fn func() int) { _, _ = Race(fn).Get() },
		"RaceWithTimeout": func(fn func() int) { _, _ = RaceWithTimeout(time.Second, fn).Get() },
		"ForEach":         func(fn func() int) { ForEach([]int{1}, 1, func(int) { fn() }) },
		"TryForEach":      func(fn func() int) { _ = TryForEach([]int{1}, 1, func(int) error { fn(); return nil }) },
		"Map":             func(fn func() int) { _, _ = Map([]int{1}, func(int) int { return fn() }) },
	}
}

// probeHelpers runs work through another helper, which must complete
// while the swept fn blocks or from inside it.
func probeHelpers() {
	_, _ = Run(func() int { return 1 }).Get()
	_, _ = All(func() int { return 1 })
}

// Every helper survives an fn that panics, blocks or re-enters. A panic is
// recovered on the goroutine that ran fn and reaches the panic hook once
// (each panic runs in a child process, so one that escapes fails its case
// alone); an fn that blocks holds nothing the other helpers need, so they
// complete meanwhile; and an fn that calls a helper again returns.
func TestHostileFn_Helpers(t *testing.T) {
	for name, helper := range sweptHelpers() {
		for _, mode := range hostile.Modes() {
			t.Run(name+"/"+mode.String(), func(t *testing.T) {
				run := func() {
					var hooked atomic.Int32
					SetPanicHook(func(context.Context, any) { hooked.Add(1) })
					t.Cleanup(func() { SetPanicHook(nil) })
					c := hostile.New(t, mode, probeHelpers)
					fn := func() int { c.Run(); return 1 }

					if mode == hostile.Block {
						done := make(chan any, 1)
						go func() { done <- hostile.Within(t, hostile.Deadline, func() { helper(fn) }) }() //safe-goroutine: Within recovers and hands the panic back through done
						select {
						case <-c.Entered():
						case <-time.After(hostile.Deadline):
							t.Fatal("fn never ran")
						}
						if p := hostile.Within(t, hostile.Deadline, probeHelpers); p != nil {
							t.Fatalf("probe panicked: %v", p)
						}
						c.Release()
						if p := <-done; p != nil {
							t.Fatalf("panic escaped %s: %v", name, p)
						}
					} else if p := hostile.Within(t, hostile.Deadline, func() { helper(fn) }); p != nil {
						t.Fatalf("panic escaped %s: %v", name, p)
					}

					want := int32(0)
					if mode == hostile.Panic {
						want = 1
						// The hook runs on fn's goroutine, which may finish
						// after the helper returned: wait for it, then give a
						// second report time to show (a slow machine can only
						// make that part pass falsely).
						for deadline := time.Now().Add(hostile.Deadline); hooked.Load() == 0 && time.Now().Before(deadline); {
							time.Sleep(time.Millisecond)
						}
						time.Sleep(20 * time.Millisecond)
					}
					if got := hooked.Load(); got != want {
						t.Errorf("panic hook calls = %d, want %d", got, want)
					}
				}
				if mode == hostile.Panic {
					hostile.Isolated(t, run)
					return
				}
				run()
			})
		}
	}
}
