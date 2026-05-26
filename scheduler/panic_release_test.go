package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// TestM35_BeforeHookPanic_LockReleased verifies that a panic inside a
// Before() hook does NOT leak the WithoutOverlapping distributed lock
// for its full TTL. Pre-fix the Before-hook loop ran outside any
// panic-recovered scope and outside any finally/defer for the lock,
// so the panic propagated up and (depending on the recovery layer)
// could leave j.running stuck and the lock held until TTL expiry.
//
// Post-fix: each Before hook is isolated in its own recover scope, and
// the top of runInternal installs a single defer that clears
// j.running and calls release() unconditionally. The test asserts the
// distributed lock is releasable immediately after runWg drains.
func TestM35_BeforeHookPanic_LockReleased(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	job := s.Named("flaky.before", func() {
		// would-be work; never reached on Before panic.
	}).Cron(cron).WithoutOverlapping()
	job.Before(func() {
		panic("before-hook intentional test panic")
	})

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(10 * time.Millisecond)

	// If the lock leaked the Acquire below would return ErrLockHeld
	// until the default 24h TTL.
	overlapKey := "velocity/scheduler/overlap:flaky.before"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after Before-hook panic: %v", err)
	}

	// j.running must also be cleared so the next tick is not gated by
	// the in-process bool.
	job.mu.RLock()
	stuck := job.running
	job.mu.RUnlock()
	if stuck {
		t.Fatal("j.running stuck true after Before-hook panic; in-process gate would block next tick")
	}
}

// TestM35_AfterHookPanic_LockReleased mirrors the Before-hook case but
// for After(). The panic happens after the main work succeeded; the
// lock and the running flag must still be cleared.
func TestM35_AfterHookPanic_LockReleased(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var workRan atomic.Bool
	s := New()
	s.SetLocker(shared)
	job := s.Named("flaky.after", func() {
		workRan.Store(true)
	}).Cron(cron).WithoutOverlapping()
	job.After(func() {
		panic("after-hook intentional test panic")
	})

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(10 * time.Millisecond)

	if !workRan.Load() {
		t.Fatal("main work must run before After hook panics")
	}

	overlapKey := "velocity/scheduler/overlap:flaky.after"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after After-hook panic: %v", err)
	}

	job.mu.RLock()
	stuck := job.running
	job.mu.RUnlock()
	if stuck {
		t.Fatal("j.running stuck true after After-hook panic")
	}
}

// TestM35_OnSuccessHookPanic_LockReleased covers the OnSuccess()
// callback panic path. Same teardown requirement.
func TestM35_OnSuccessHookPanic_LockReleased(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	job := s.Named("flaky.onsuccess", func() {
		// successful work
	}).Cron(cron).WithoutOverlapping()
	job.OnSuccess(func() {
		panic("on-success-hook intentional test panic")
	})

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(10 * time.Millisecond)

	overlapKey := "velocity/scheduler/overlap:flaky.onsuccess"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after OnSuccess panic: %v", err)
	}
}

// TestM35_OnFailureHookPanic_LockReleased covers OnFailure() panic
// after the main work itself returned an error. Both panics must
// unwind cleanly without leaking the lock.
func TestM35_OnFailureHookPanic_LockReleased(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	job := s.NamedE("flaky.onfailure", func() error {
		return fmt.Errorf("intentional work failure")
	}).Cron(cron).WithoutOverlapping()
	job.OnFailure(func(err error) {
		panic("on-failure-hook intentional test panic")
	})

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(10 * time.Millisecond)

	overlapKey := "velocity/scheduler/overlap:flaky.onfailure"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after OnFailure panic: %v", err)
	}
}

// TestM35_MultipleBeforeHooks_PanicIsolated proves that a panic in
// one Before hook does NOT skip the remaining Before hooks. Each
// hook is isolated in its own recover scope so one bad listener does
// not silently break the others. This is the same property the events
// dispatcher provides for event listeners.
func TestM35_MultipleBeforeHooks_PanicIsolated(t *testing.T) {
	t.Parallel()

	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var first, third atomic.Bool

	s := New()
	job := s.Named("multihook", func() {}).Cron(cron)
	job.Before(func() { first.Store(true) })
	job.Before(func() { panic("middle hook boom") })
	job.Before(func() { third.Store(true) })

	s.runDueJobs()
	s.runWg.Wait()

	if !first.Load() {
		t.Error("first Before hook must run")
	}
	if !third.Load() {
		t.Error("third Before hook must run despite middle panic")
	}
}

// TestM35_HandlerPanic_LockReleased pins the original C-04 follow-on
// case (the job callback itself panics). Already covered by
// TestRunDueJobs_LockReleasedOnPanic in locker_wiring_test.go; we
// keep this here as the M-35 explicit lock-defer assertion to make
// the contract visible.
func TestM35_HandlerPanic_LockReleased(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	s.Named("flaky.handler", func() {
		panic("handler intentional test panic")
	}).Cron(cron).WithoutOverlapping()

	s.runDueJobs()
	s.runWg.Wait()
	time.Sleep(10 * time.Millisecond)

	overlapKey := "velocity/scheduler/overlap:flaky.handler"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after handler panic: %v", err)
	}
}
