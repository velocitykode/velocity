package buildonce

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// Many goroutines: every one runs its own function, never two at once.
func TestSerial_RunsEachCallersFunctionOneAtATime(t *testing.T) {
	const callers = 64
	var s Serial
	var running, ran atomic.Int32
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			err := s.Do(context.Background(), func() {
				if n := running.Add(1); n != 1 {
					t.Errorf("%d functions running at once", n)
				}
				ran.Add(1)
				running.Add(-1)
			})
			if err != nil {
				t.Errorf("Do: %v", err)
			}
		})
	}
	hostile.Within(t, hostile.Deadline, wg.Wait)
	if n := ran.Load(); n != callers {
		t.Fatalf("%d functions ran, want every caller's own (%d)", n, callers)
	}
}

// A waiter leaves at its ctx without running its function; the turn in
// progress carries on and the next Do gets the turn.
func TestSerial_WaiterHonoursCtx(t *testing.T) {
	var s Serial
	started, release := make(chan struct{}), make(chan struct{})
	held := make(chan struct{})
	go func() {
		defer close(held)
		_ = s.Do(context.Background(), func() {
			close(started)
			<-release
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		waited <- s.Do(ctx, func() { t.Error("a waiter whose ctx ended ran its function") })
	}()
	hostile.Eventually(t, hostile.Deadline, "the waiter parking", func() bool { return s.Waiting() == 1 })
	cancel()
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-waited })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter whose ctx ended returned %v, want context.Canceled", err)
	}
	if n := s.Waiting(); n != 0 {
		t.Fatalf("%d waiters after the only one left", n)
	}
	close(release)
	hostile.Within(t, hostile.Deadline, func() { <-held })
	ran := false
	if err := s.Do(context.Background(), func() { ran = true }); err != nil || !ran {
		t.Fatalf("Do after the turn ended: ran=%v err=%v", ran, err)
	}
}

// A goroutine inside another flight (another Serial's turn, a Group's
// build) waits for the turn like any other caller and then runs its own
// function: a Serial refuses nobody.
func TestSerial_AnotherFlightOnTheStackWaits(t *testing.T) {
	enter := map[string]func(fn func()){
		"another Serial's turn": func(fn func()) {
			var other Serial
			_ = other.Do(context.Background(), fn)
		},
		"a Group's build": func(fn func()) {
			var g Group[int]
			_, _ = g.Do(context.Background(), "k", func() (int, error) { fn(); return 0, nil })
		},
	}
	for flight, inFlight := range enter {
		t.Run(flight, func(t *testing.T) {
			var s Serial
			started, release := make(chan struct{}), make(chan struct{})
			held := make(chan struct{})
			go func() {
				defer close(held)
				_ = s.Do(context.Background(), func() {
					close(started)
					<-release
				})
			}()
			<-started

			var holderRunning atomic.Bool
			holderRunning.Store(true)
			nested := make(chan error, 1)
			ran := false
			go func() {
				inFlight(func() {
					nested <- s.Do(context.Background(), func() {
						if holderRunning.Load() {
							t.Error("the nested function ran while the turn was held")
						}
						ran = true
					})
				})
			}()
			hostile.Eventually(t, hostile.Deadline, "the caller inside another flight waiting for the turn", func() bool {
				return s.Waiting() == 1
			})
			holderRunning.Store(false)
			close(release)
			var err error
			hostile.Within(t, hostile.Deadline, func() { err = <-nested; <-held })
			if err != nil || !ran {
				t.Fatalf("a Do from inside another flight: ran=%v err=%v; want it to wait for the turn and run", ran, err)
			}
		})
	}
}

// A function that asks for its own turn again waits on itself: a Serial
// does not know who holds the turn. It leaves at its context, and the
// turn it is inside of carries on.
func TestSerial_OwnTurnWaitsUntilItsContextEnds(t *testing.T) {
	var s Serial
	var nested error
	outerFinished := false
	hostile.Within(t, hostile.Deadline, func() {
		_ = s.Do(context.Background(), func() {
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				hostile.Eventually(t, hostile.Deadline, "the nested Do waiting on its own turn", func() bool { return s.Waiting() == 1 })
				cancel()
			}()
			nested = s.Do(ctx, func() { t.Error("the nested function ran inside its own turn") })
			<-done
			outerFinished = true
		})
	})
	if !errors.Is(nested, context.Canceled) {
		t.Fatalf("a Do from inside its own turn returned %v, want it to wait and leave at its context", nested)
	}
	if !outerFinished {
		t.Fatal("the outer turn did not carry on")
	}
}

// A free turn is taken from inside a build.
func TestSerial_FreeTurnIsTakenFromInsideABuild(t *testing.T) {
	var s Serial
	var g Group[int]
	ran := false
	_, _ = g.Do(context.Background(), "k", func() (int, error) {
		return 0, s.Do(context.Background(), func() { ran = true })
	})
	if !ran {
		t.Fatal("a free turn was not taken from inside a build")
	}
}

// A function that panics frees the turn; the panic reaches Do's caller.
func TestSerial_PanicFreesTheTurn(t *testing.T) {
	var s Serial
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach Do's caller")
			}
		}()
		_ = s.Do(context.Background(), func() { panic("boom") })
	}()
	ran := false
	hostile.Within(t, hostile.Deadline, func() {
		_ = s.Do(context.Background(), func() { ran = true })
	})
	if !ran {
		t.Fatal("the turn stayed taken after a panic")
	}
}

// BenchmarkSerial_Uncontended is a Do that finds the turn free: the path
// every transition of a value nobody else is changing takes. It is in the
// zero-allocation set (scripts/ci/check-zero-alloc-benchmarks.sh).
func BenchmarkSerial_Uncontended(b *testing.B) {
	var s Serial
	ctx := context.Background()
	n := 0
	b.ReportAllocs()
	for b.Loop() {
		_ = s.Do(ctx, func() { n++ })
	}
}
