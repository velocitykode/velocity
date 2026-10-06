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

// endsWhileParked is a context that ends when the test says so without
// waking a waiter already parked on it: Done returns a channel that never
// closes until end is called and a closed one after. It puts a waiter in
// the state a real context puts it in when the context ends and the turn is
// released before the waiter runs again: woken by the turn, context ended.
type endsWhileParked struct {
	context.Context
	ended  atomic.Bool
	open   chan struct{}
	closed chan struct{}
}

func newEndsWhileParked() *endsWhileParked {
	c := &endsWhileParked{Context: context.Background(), open: make(chan struct{}), closed: make(chan struct{})}
	close(c.closed)
	return c
}

func (c *endsWhileParked) end() { c.ended.Store(true) }

func (c *endsWhileParked) Done() <-chan struct{} {
	if c.ended.Load() {
		return c.closed
	}
	return c.open
}

func (c *endsWhileParked) Err() error {
	if c.ended.Load() {
		return context.Canceled
	}
	return nil
}

// A waiter whose ctx ended while it was parked, and which the released turn
// then wakes, leaves with ctx.Err() and does not run its function: the turn
// stays free for the next caller.
func TestSerial_WaiterWokenAfterItsCtxEndedDoesNotRun(t *testing.T) {
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

	ctx := newEndsWhileParked()
	var ran atomic.Bool
	waited := make(chan error, 1)
	go func() {
		waited <- s.Do(ctx, func() { ran.Store(true) })
	}()
	hostile.Eventually(t, hostile.Deadline, "the waiter parking", func() bool { return s.Waiting() == 1 })
	ctx.end()
	close(release)
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-waited; <-held })
	if ran.Load() {
		t.Fatal("a waiter woken by the released turn ran its function after its ctx had ended")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiter woken after its ctx ended returned %v, want context.Canceled", err)
	}
	if n := s.Waiting(); n != 0 {
		t.Fatalf("%d waiters after the only one left", n)
	}
	next := false
	hostile.Within(t, hostile.Deadline, func() {
		if err := s.Do(context.Background(), func() { next = true }); err != nil {
			t.Errorf("Do after the waiter left: %v", err)
		}
	})
	if !next {
		t.Fatal("the turn the waiter did not take was not free for the next caller")
	}
}

// Many waiters behind one turn, half of them with a ctx that ends as the
// turn is released: every Do either runs its function once and returns nil
// or returns its ctx's error without running it, never two at once, and
// nobody is left waiting.
func TestSerial_Concurrent_WaitersLeavingAsTheTurnIsReleased(t *testing.T) {
	const waiters = 32
	for range 50 {
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
		var running, ran, left atomic.Int32
		var wg sync.WaitGroup
		for i := range waiters {
			c := context.Background()
			if i%2 == 0 {
				c = ctx
			}
			wg.Go(func() {
				did := false
				err := s.Do(c, func() {
					if n := running.Add(1); n != 1 {
						t.Errorf("%d functions running at once", n)
					}
					did = true
					ran.Add(1)
					running.Add(-1)
				})
				switch {
				case err == nil && !did:
					t.Error("Do returned nil without running its function")
				case err != nil && did:
					t.Errorf("Do ran its function and returned %v", err)
				case err != nil:
					left.Add(1)
				}
			})
		}
		hostile.Eventually(t, hostile.Deadline, "every waiter parking", func() bool { return s.Waiting() == waiters })
		go cancel()
		close(release)
		hostile.Within(t, hostile.Deadline, func() { wg.Wait(); <-held })
		if got := ran.Load() + left.Load(); got != waiters {
			t.Fatalf("%d ran and %d left, want %d callers accounted for", ran.Load(), left.Load(), waiters)
		}
		if ran.Load() < waiters/2 {
			t.Fatalf("%d functions ran, want at least the %d whose ctx never ends", ran.Load(), waiters/2)
		}
		if n := s.Waiting(); n != 0 {
			t.Fatalf("%d waiters left behind", n)
		}
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

// BenchmarkSerial_Contended is Do with callers on every P asking for one
// turn: the path a caller takes that finds the turn held, parks and is
// woken by its release.
func BenchmarkSerial_Contended(b *testing.B) {
	var s Serial
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = s.Do(ctx, func() { n++ })
		}
	})
}
