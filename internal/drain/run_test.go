package drain_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A unit admitted before Close keeps the run from going idle until it is
// released; nothing is admitted after Close; exactly one Close wins.
func TestRun_AdmissionClosesWithTheStop(t *testing.T) {
	var o drain.Owner
	r := o.NewRun()
	if !r.Admit() {
		t.Fatal("Admit before Close = false")
	}
	if r.Stopping() {
		t.Fatal("Stopping before Close")
	}
	if !r.Close() {
		t.Fatal("first Close = false, want true")
	}
	if !r.Stopping() {
		t.Fatal("not Stopping after Close")
	}
	if r.Close() {
		t.Error("second Close = true: two stops would own the run")
	}
	if r.Admit() {
		t.Error("Admit after Close = true: a unit would start after the stop began waiting")
	}
	if drain.Closed(r.Idle()) {
		t.Fatal("idle while an admitted unit is unreleased")
	}
	r.Release()
	if !drain.Closed(r.Idle()) {
		t.Fatal("not idle once the last unit was released")
	}
}

// A unit joined under a held unit counts even after Close, and the run
// goes idle only once both are released.
func TestRun_JoinUnderAHeldUnit(t *testing.T) {
	var o drain.Owner
	r := o.NewRun()
	if !r.Admit() {
		t.Fatal("Admit = false")
	}
	r.Close()
	r.Join()
	r.Release()
	if drain.Closed(r.Idle()) {
		t.Fatal("idle while a joined unit is unreleased")
	}
	r.Release()
	if !drain.Closed(r.Idle()) {
		t.Fatal("not idle once every unit was released")
	}
}

// A run nobody admitted into is idle as soon as it closes.
func TestRun_IdleAtCloseWithNothingAdmitted(t *testing.T) {
	var o drain.Owner
	r := o.NewRun()
	if drain.Closed(r.Idle()) {
		t.Fatal("idle before Close")
	}
	r.Close()
	if !drain.Closed(r.Idle()) {
		t.Fatal("not idle at Close with nothing admitted")
	}
}

// Admit, Release and Close race from many goroutines: exactly one Close
// wins, every Admit that succeeded is counted before Idle closes, and no
// Admit succeeds after Close returned.
func TestRun_AdmissionRace(t *testing.T) {
	for range 200 {
		var o drain.Owner
		r := o.NewRun()
		var (
			wg       sync.WaitGroup
			winners  atomic.Int32
			inside   atomic.Int32
			closed   atomic.Bool
			lateUnit atomic.Bool
		)
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 50 {
					if !r.Admit() {
						return
					}
					if closed.Load() && drain.Closed(r.Idle()) {
						lateUnit.Store(true)
					}
					inside.Add(1)
					inside.Add(-1)
					r.Release()
				}
			}()
		}
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if r.Close() {
					winners.Add(1)
					closed.Store(true)
				}
			}()
		}
		wg.Wait()
		r.Close()
		<-r.Idle()
		if n := winners.Load(); n != 1 {
			t.Fatalf("%d Close winners, want 1", n)
		}
		if inside.Load() != 0 || lateUnit.Load() {
			t.Fatal("a unit ran while the run was idle")
		}
	}
}

// Finish publishes the first result only, and Await returns it to every
// stop, before and after, however many.
func TestRun_AwaitReturnsTheRetainedResult(t *testing.T) {
	var o drain.Owner
	r := o.NewRun()
	want := errors.New("first")
	r.Finish(want)
	r.Finish(errors.New("second"))
	for range 3 {
		if err := r.Await(context.Background(), nil); err != want {
			t.Fatalf("Await = %v, want %v", err, want)
		}
	}
	//lint:ignore SA1012 a nil ctx is Await's documented unbounded wait
	if err := r.Await(nil, nil); err != want {
		t.Fatalf("Await(nil) = %v, want %v", err, want)
	}
}

// At its ctx, Await returns ctx's error and never the (absent) result;
// the run's force is claimed once however many Awaits time out, and it
// runs as the owner's work.
func TestRun_AwaitForcesOncePerRun(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o drain.Owner
		r := o.NewRun()
		release := make(chan struct{})
		var forces, nested atomic.Int32
		force := func() {
			forces.Add(1)
			if o.Nested() {
				nested.Add(1)
			}
			<-release
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for range 5 {
			if err := r.Await(ctx, force); !errors.Is(err, context.Canceled) {
				t.Fatalf("Await = %v, want the ctx error", err)
			}
		}
		synctest.Wait()
		if forces.Load() != 1 || nested.Load() != 1 {
			t.Errorf("force ran %d times (%d as own work), want once as own work", forces.Load(), nested.Load())
		}
		close(release)
		r.Finish(nil)
		if err := r.Await(ctx, force); err != nil {
			t.Errorf("Await after Finish with a done ctx = %v, want the result", err)
		}
	})
}

// Stop: the first runs work once, on a goroutine of its own recorded as
// own work, and every Stop, overlapping or later, gets its result.
func TestOwner_StopRunsWorkOnceAndRetainsItsResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o drain.Owner
		r := o.NewRun()
		want := errors.New("closed with an error")
		gate := make(chan struct{})
		var runs atomic.Int32
		var ownWork atomic.Bool
		work := func() error {
			runs.Add(1)
			ownWork.Store(o.Nested())
			<-gate
			return want
		}
		errs := make(chan error, 3)
		for range 3 {
			go func() { errs <- o.Stop(context.Background(), r, work, nil) }()
		}
		synctest.Wait()
		if o.Nested() {
			t.Fatal("the test goroutine is own work")
		}
		close(gate)
		for range 3 {
			if err := <-errs; err != want {
				t.Errorf("Stop = %v, want %v", err, want)
			}
		}
		if runs.Load() != 1 || !ownWork.Load() {
			t.Errorf("work ran %d times (own work %v), want once as own work", runs.Load(), ownWork.Load())
		}
		if err := o.Stop(context.Background(), r, work, nil); err != want {
			t.Errorf("later Stop = %v, want %v", err, want)
		}
	})
}

// A Stop whose ctx ends first returns ctx's error and leaves the work
// running; the next Stop gets the work's result, not nil early.
func TestOwner_StopAtItsCtxLeavesTheWorkRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var o drain.Owner
		r := o.NewRun()
		gate := make(chan struct{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := o.Stop(ctx, r, func() error { <-gate; return nil }, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Stop = %v, want the ctx error", err)
		}
		synctest.Wait()
		if drain.Closed(r.Finished()) {
			t.Fatal("run finished while its work is blocked")
		}
		ctx2, cancel2 := context.WithCancel(context.Background())
		defer cancel2()
		got := make(chan error, 1)
		go func() { got <- o.Stop(ctx2, r, nil, nil) }()
		synctest.Wait()
		select {
		case err := <-got:
			t.Fatalf("second Stop returned %v before the work finished", err)
		default:
		}
		close(gate)
		if err := <-got; err != nil {
			t.Fatalf("second Stop = %v, want the work's nil", err)
		}
	})
}

// The hostile sweep over the stop's work: a panic is its result, a block
// holds no caller past ctx, and a Stop the work calls back is refused at
// once without waiting on itself.
func TestOwner_StopHostileWork(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			var o drain.Owner
			r := o.NewRun()
			var reentered error
			code := hostile.New(t, mode, func() {
				reentered = o.Stop(context.Background(), r, nil, nil)
			})
			var got error
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hostile.Within(t, hostile.Deadline, func() {
				if mode == hostile.Block {
					cancel()
				}
				got = o.Stop(ctx, r, func() error { code.Run(); return nil }, nil)
			})
			switch mode {
			case hostile.Panic:
				if got == nil {
					t.Fatal("Stop = nil for work that panicked")
				}
			case hostile.Block:
				if !errors.Is(got, context.Canceled) {
					t.Fatalf("Stop = %v, want the ctx error", got)
				}
				code.Release()
				if err := r.Await(context.Background(), nil); err != nil {
					t.Fatalf("result after release = %v", err)
				}
			case hostile.Reenter:
				if got != nil {
					t.Fatalf("Stop = %v, want nil", got)
				}
				if !errors.Is(reentered, contract.ErrStopFromOwnWork) {
					t.Fatalf("re-entered Stop = %v, want ErrStopFromOwnWork", reentered)
				}
			}
		})
	}
}

// Once the run finished, a Stop from own work gets the result instead of
// the refusal: it no longer waits on anything.
func TestOwner_NestedStopAfterFinishGetsTheResult(t *testing.T) {
	var o drain.Owner
	r := o.NewRun()
	want := errors.New("done")
	if err := o.Stop(context.Background(), r, func() error { return want }, nil); err != want {
		t.Fatalf("Stop = %v", err)
	}
	var got error
	o.Do(func() { got = o.Stop(context.Background(), r, nil, nil) })
	if got != want {
		t.Fatalf("nested Stop after finish = %v, want %v", got, want)
	}
}
