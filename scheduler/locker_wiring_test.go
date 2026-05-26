package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunDueJobs_OnOneServer_OnlyOneInstanceRuns covers C-04 (F2): two
// independent *Scheduler instances backed by the SAME Locker must elect a
// single winner per scheduled tick for jobs flagged OnOneServer(). The
// non-winner skips silently.
//
// Without the fix, j.onOneServer was set but never read, so both
// schedulers fired the job on every tick.
func TestRunDueJobs_OnOneServer_OnlyOneInstanceRuns(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var counter atomic.Int32
	work := func() { counter.Add(1) }

	hostA := New()
	hostA.SetLocker(shared)
	hostA.Named("billing.run", work).Cron(cron).OnOneServer()

	hostB := New()
	hostB.SetLocker(shared)
	hostB.Named("billing.run", work).Cron(cron).OnOneServer()

	// Both schedulers evaluate the same minute. Exactly one must execute
	// the job; the other must observe the lock and skip.
	hostA.runDueJobs()
	hostB.runDueJobs()

	hostA.runWg.Wait()
	hostB.runWg.Wait()

	if got := counter.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution across both hosts, got %d", got)
	}
}

// TestRunDueJobs_OnOneServer_NextMinuteReContests verifies that the
// OnOneServer lock key embeds the scheduled minute so the next cron tick
// gets a fresh contest. A lock held for tick T must not gate tick T+1.
func TestRunDueJobs_OnOneServer_NextMinuteReContests(t *testing.T) {
	t.Parallel()

	job := &Job{
		name:        "billing.run",
		schedule:    &Schedule{},
		timezone:    time.UTC,
		onOneServer: true,
	}

	// Two distinct minutes produce two distinct keys.
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	next := now.Add(time.Minute)

	keyNow := job.oneServerLockKey(now)
	keyNext := job.oneServerLockKey(next)
	if keyNow == keyNext {
		t.Fatalf("expected per-minute key rotation; got %q == %q", keyNow, keyNext)
	}

	// Holding the lock for tick T must NOT prevent acquiring tick T+1.
	loc := NewInMemoryLocker()
	if _, err := loc.Acquire(context.Background(), keyNow, time.Hour); err != nil {
		t.Fatalf("acquire T: %v", err)
	}
	if _, err := loc.Acquire(context.Background(), keyNext, time.Hour); err != nil {
		t.Fatalf("acquire T+1 must succeed (different key), got: %v", err)
	}
}

// TestRunDueJobs_MaintenanceMode_RespectsEvenInMaintenanceMode covers the
// second-opinion finding that EvenInMaintenanceMode() was a no-op: the
// scheduler returned before iterating jobs when MaintenanceMode was on,
// so flagged jobs never ran. Now the gate is per-job.
func TestRunDueJobs_MaintenanceMode_RespectsEvenInMaintenanceMode(t *testing.T) {
	t.Parallel()

	s := New()
	s.MaintenanceMode(true)

	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var maintenanceRan, normalRan atomic.Int32
	s.Named("ops.health", func() {
		maintenanceRan.Add(1)
	}).Cron(cron).EvenInMaintenanceMode()

	s.Named("ops.cleanup", func() {
		normalRan.Add(1)
	}).Cron(cron)

	s.runDueJobs()
	s.runWg.Wait()

	if maintenanceRan.Load() != 1 {
		t.Errorf("EvenInMaintenanceMode job must run during maintenance; got %d", maintenanceRan.Load())
	}
	if normalRan.Load() != 0 {
		t.Errorf("non-flagged job must NOT run during maintenance; got %d", normalRan.Load())
	}
}

// TestRunDueJobs_WithoutOverlapping_AcrossSchedulers verifies that the
// WithoutOverlapping distributed lock prevents overlap across two
// independent *Scheduler instances (i.e. cross-process / cross-host
// semantics) sharing a Locker. Pre-fix, j.running was an in-process bool
// so a second scheduler instance happily fired the same job.
func TestRunDueJobs_WithoutOverlapping_AcrossSchedulers(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var counter atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	work := func() {
		if counter.Add(1) == 1 {
			started <- struct{}{}
			<-release
		}
	}

	hostA := New()
	hostA.SetLocker(shared)
	hostA.Named("report.daily", work).Cron(cron).WithoutOverlapping()

	hostB := New()
	hostB.SetLocker(shared)
	hostB.Named("report.daily", work).Cron(cron).WithoutOverlapping()

	hostA.runDueJobs()
	// Wait for hostA's job to be actively running.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("hostA's job did not start")
	}

	hostB.runDueJobs()
	// hostB must see lock held and skip immediately.
	hostB.runWg.Wait()

	if got := counter.Load(); got != 1 {
		t.Fatalf("expected exactly 1 execution while hostA's job still running, got %d", got)
	}

	close(release)
	hostA.runWg.Wait()
}

// TestRunDueJobs_LockReleasedOnPanic verifies that a panicking job hook
// does NOT leak the WithoutOverlapping distributed lock for its full TTL.
// Pre-fix this was the F4 follow-on hazard: once the Locker is wired, a
// panic in a Before callback would bypass Release. The deferred
// panic-safe release in runDueJobs must always run.
//
// The OnOneServer lock is deliberately retained until its TTL expires
// (its key embeds the scheduled minute so the next tick gets a fresh
// contest regardless). Only WithoutOverlapping releases on panic.
func TestRunDueJobs_LockReleasedOnPanic(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	// A job whose callback panics. Job.Run's internal recover catches
	// the panic and dispatches scheduled.failed; the test verifies the
	// WithoutOverlapping lock is released on the way out. If it leaked
	// the Acquire below would fail until the (default 24h) TTL elapses.
	s.Named("flaky.job", func() {
		panic("intentional test panic")
	}).Cron(cron).WithoutOverlapping()

	s.runDueJobs()
	s.runWg.Wait()

	// Tiny grace for any backend bookkeeping after runWg.Done.
	time.Sleep(10 * time.Millisecond)

	ctx := context.Background()
	overlapKey := "velocity/scheduler/overlap:flaky.job"
	if _, err := shared.Acquire(ctx, overlapKey, time.Second); err != nil {
		t.Fatalf("overlap lock leaked after panic: %v", err)
	}

	// A second scheduler with a different job sharing the SAME locker
	// must still run (the drain proves the panic did not corrupt
	// locker state for unrelated keys).
	var ran atomic.Int32
	s2 := New()
	s2.SetLocker(shared)
	s2.Named("healthy.job", func() { ran.Add(1) }).Cron(cron).WithoutOverlapping().OnOneServer()
	s2.runDueJobs()
	s2.runWg.Wait()

	if ran.Load() != 1 {
		t.Fatalf("expected fresh job to run after panic on different job; got %d", ran.Load())
	}
}

// TestRunDueJobs_OnOneServer_LockHeldUntilTTL verifies the converse of
// the panic-release behaviour above: the OnOneServer lock is NOT released
// on job completion. Holding it for the full TTL is what gives the
// per-minute "exactly one host runs this tick" semantic; releasing it
// would let a fast-finishing job on host A allow host B to re-acquire
// the same minute's slot before the next tick boundary.
func TestRunDueJobs_OnOneServer_LockHeldUntilTTL(t *testing.T) {
	t.Parallel()

	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)

	var counter atomic.Int32
	s.Named("billing.tick", func() {
		counter.Add(1)
	}).Cron(cron).OnOneServer()

	// Run twice in the same minute. The first acquires the minute-keyed
	// lock; the second must observe it as held and skip even though the
	// first job has already finished executing.
	s.runDueJobs()
	s.runWg.Wait()
	s.runDueJobs()
	s.runWg.Wait()

	if got := counter.Load(); got != 1 {
		t.Fatalf("OnOneServer lock must be held until TTL so repeat ticks in the same minute do not re-run; got %d executions", got)
	}
}

// TestSetLocker_NilFallsBackToInMemory verifies the documented contract
// that SetLocker(nil) installs a process-local InMemoryLocker, so callers
// can "reset" rather than leave a nil reference that would crash
// runDueJobs.
func TestSetLocker_NilFallsBackToInMemory(t *testing.T) {
	t.Parallel()

	s := New()
	s.SetLocker(nil)

	s.mu.RLock()
	got := s.locker
	s.mu.RUnlock()

	if got == nil {
		t.Fatal("SetLocker(nil) must install a fallback Locker, got nil")
	}
	if _, ok := got.(*InMemoryLocker); !ok {
		t.Fatalf("expected *InMemoryLocker fallback, got %T", got)
	}
}

// TestRunDueJobs_OnOneServer_ConcurrentSchedulers stress-tests the
// per-tick contest with N goroutines each running its own scheduler
// against the same Locker. Exactly one execution per "tick" must win.
func TestRunDueJobs_OnOneServer_ConcurrentSchedulers(t *testing.T) {
	t.Parallel()

	const hosts = 8
	shared := NewInMemoryLocker()
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	var counter atomic.Int32
	var wg sync.WaitGroup

	schedulers := make([]*Scheduler, hosts)
	for i := 0; i < hosts; i++ {
		s := New()
		s.SetLocker(shared)
		s.Named("cluster.cron", func() {
			counter.Add(1)
		}).Cron(cron).OnOneServer()
		schedulers[i] = s
	}

	// Fire all schedulers concurrently to maximize contention.
	for _, s := range schedulers {
		wg.Add(1)
		go func(s *Scheduler) {
			defer wg.Done()
			s.runDueJobs()
		}(s)
	}
	wg.Wait()

	for _, s := range schedulers {
		s.runWg.Wait()
	}

	if got := counter.Load(); got != 1 {
		t.Fatalf("OnOneServer contest must elect exactly one winner across %d hosts; got %d executions", hosts, got)
	}
}
