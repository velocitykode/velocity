package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// blockingLocker is a Locker whose Acquire blocks until either
// (a) the test signals it via release, or (b) ctx is cancelled. It
// pins the regression for the HIGH-1 race: a slow remote backend
// (Redis network hiccup, DB advisory lock contention) must NOT let a
// lock acquisition succeed AFTER the scheduler has signaled "no more
// dispatch". The fix uses the scheduler's runCtx for Acquire so a
// pending acquire returns ctx.Err() promptly on Shutdown.
type blockingLocker struct {
	// proceed gates Acquire. The test closes it to let a normal
	// acquire complete; before that, Acquire selects on it vs ctx.Done.
	proceed chan struct{}
	// inflight counts the number of in-progress Acquire calls so the
	// test can wait until the scheduler is actually parked inside
	// Acquire before triggering Shutdown.
	inflight atomic.Int32
	// observedCtxCancel becomes true if any Acquire observed its ctx
	// being cancelled (i.e. Shutdown propagated). The HIGH-1
	// regression test asserts this.
	observedCtxCancel atomic.Bool
}

func newBlockingLocker() *blockingLocker {
	return &blockingLocker{proceed: make(chan struct{})}
}

func (l *blockingLocker) Acquire(ctx context.Context, name string, ttl time.Duration) (Lock, error) {
	l.inflight.Add(1)
	defer l.inflight.Add(-1)
	select {
	case <-l.proceed:
		// Normal completion: return a no-op lock so the scheduler can
		// dispatch as usual. The test usually does not take this path
		// (it cancels ctx instead), but it's here for completeness.
		return &noopLock{name: name}, nil
	case <-ctx.Done():
		l.observedCtxCancel.Store(true)
		return nil, ctx.Err()
	}
}

// noopLock is a Lock that records nothing. Used by tests that only
// care about whether Acquire returned, not what happened next.
type noopLock struct{ name string }

func (l *noopLock) Name() string                    { return l.name }
func (l *noopLock) FencingToken() uint64            { return 1 }
func (l *noopLock) Release(_ context.Context) error { return nil }

// TestShutdown_CancelsInflightLockAcquire is the HIGH-1 regression: a
// Scheduler running against a slow Locker must, on Shutdown, propagate
// cancellation into the in-flight Acquire so the call returns ctx.Err
// promptly. Without the fix, Acquire ran under context.Background()
// and could succeed AFTER Shutdown returned -- the resulting job
// goroutine outlived the scheduler.
func TestShutdown_CancelsInflightLockAcquire(t *testing.T) {
	t.Parallel()

	loc := newBlockingLocker()

	s := New()
	s.SetLocker(loc)
	// Job is OnOneServer so runDueJobs hits Acquire.
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())
	s.Named("stuck.job", func() { /* must never run */ }).Cron(cron).OnOneServer()

	runErr := make(chan error, 1)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() {
		runErr <- s.Run(runCtx)
	}()

	// Wait until the scheduler is actually parked inside Acquire.
	deadline := time.Now().Add(2 * time.Second)
	for loc.inflight.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if loc.inflight.Load() == 0 {
		t.Fatal("scheduler did not enter Locker.Acquire within 2s")
	}

	// Trigger shutdown via the scheduler's API. This MUST cancel the
	// in-flight Acquire so it returns promptly.
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownDone <- s.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned non-nil error (runWg did not drain): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not return within 2s; runWg leaked an acquire-pending slot")
	}

	if !loc.observedCtxCancel.Load() {
		t.Fatal("Locker.Acquire did not observe ctx cancellation; Shutdown did not propagate runCtx cancel")
	}

	// runCancel + drain Run goroutine.
	runCancel()
	select {
	case <-runErr:
	case <-time.After(2 * time.Second):
		t.Fatal("Scheduler.Run did not return after cancel")
	}
}

// countingErrLocker is a Locker that fails Acquire deterministically
// with the given error. Used to verify the runWg is balanced on the
// error-return path: every Add must be matched by a Done even when
// Acquire never returns a Lock.
type countingErrLocker struct {
	err      error
	attempts atomic.Int32
}

func (l *countingErrLocker) Acquire(_ context.Context, _ string, _ time.Duration) (Lock, error) {
	l.attempts.Add(1)
	return nil, l.err
}

// TestRunDueJobs_RunWgBalancedOnAcquireFailure pins the invariant that
// runWg.Add(1) taken before Acquire is balanced by runWg.Done() on the
// error path. A leaked Add would make Shutdown.Wait() block forever.
func TestRunDueJobs_RunWgBalancedOnAcquireFailure(t *testing.T) {
	t.Parallel()

	loc := &countingErrLocker{err: ErrLockHeld}

	s := New()
	s.SetLocker(loc)
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())
	// Two jobs to exercise the loop more than once.
	s.Named("a", func() {}).Cron(cron).OnOneServer()
	s.Named("b", func() {}).Cron(cron).WithoutOverlapping()

	s.runDueJobs()

	if loc.attempts.Load() == 0 {
		t.Fatal("expected Locker.Acquire to be called")
	}

	// Shutdown of a non-running scheduler is a no-op, so we test the
	// runWg balance directly: it must already be at zero (no pending
	// Adds) immediately after runDueJobs returns.
	done := make(chan struct{})
	go func() {
		s.runWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWg leaked an Add on the Acquire-failure path")
	}
}

// TestRunInBackground_LockHeldUntilProcessExit is the HIGH-2
// regression. RunInBackground().WithoutOverlapping() previously
// released the overlap lock at cmd.Start() return; the OS process
// kept running, but the next tick could re-acquire and dispatch a
// SECOND process concurrently. The fix transfers lock ownership to a
// waiter goroutine that releases only after cmd.Wait completes.
func TestRunInBackground_LockHeldUntilProcessExit(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("sleep command shape differs on Windows")
	}

	shared := NewInMemoryLocker()

	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())

	s := New()
	s.SetLocker(shared)
	// Sleep 0.8s in the background so the test has a generous window
	// to attempt a second dispatch while the first is still alive.
	s.Command("sleep", "0.8").Name("bg.job").Cron(cron).RunInBackground().WithoutOverlapping()

	// First dispatch: lock acquired, process started.
	s.runDueJobs()

	// Give the dispatch goroutine + cmd.Start a brief moment so the
	// waiter goroutine has actually taken over the lock.
	time.Sleep(50 * time.Millisecond)

	// Attempt to acquire the SAME overlap key directly. Pre-fix, the
	// lock would already be released (cmd.Start returned), so this
	// acquire would succeed -- meaning the next scheduler tick could
	// double-dispatch. Post-fix, the waiter still holds it.
	overlapKey := "velocity/scheduler/overlap:bg.job"
	if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("expected ErrLockHeld while bg process running, got %v", err)
	}

	// Wait until the waiter releases. cmd ~0.8s + small slack.
	deadline := time.Now().Add(3 * time.Second)
	var freed bool
	for time.Now().Before(deadline) {
		if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); err == nil {
			freed = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !freed {
		t.Fatal("waiter goroutine did not release overlap lock after cmd.Wait completed")
	}

	// Drain any in-flight scheduler bookkeeping.
	s.runWg.Wait()
}

// TestRunInBackground_ShutdownSIGTERMsThenKills verifies the documented
// shutdown semantic: a RunInBackground process receives SIGTERM on
// scheduler runCtx cancellation and is SIGKILLed after shutdownGrace if
// it has not exited. The waiter goroutine MUST drain so Shutdown
// returns nil promptly.
func TestRunInBackground_ShutdownSIGTERMsThenKills(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ on Windows")
	}

	s := New()
	// Compressed grace so the test finishes quickly.
	s.shutdownGrace = 100 * time.Millisecond

	// A long-running sleep that we'll signal during shutdown.
	cron := fmt.Sprintf("%d * * * *", time.Now().Minute())
	s.Command("sleep", "30").Name("longrun").Cron(cron).RunInBackground().WithoutOverlapping()

	runErr := make(chan error, 1)
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	go func() {
		runErr <- s.Run(runCtx)
	}()

	// Wait for the bg process to actually start. We probe the overlap
	// lock: while the waiter holds it, ErrLockHeld is the signal that
	// the process is live.
	shared := s.Locker()
	overlapKey := "velocity/scheduler/overlap:longrun"
	deadline := time.Now().Add(2 * time.Second)
	var started bool
	for time.Now().Before(deadline) {
		if _, err := shared.Acquire(context.Background(), overlapKey, time.Second); errors.Is(err, ErrLockHeld) {
			started = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !started {
		// Not a fail of the test invariant (process may have errored
		// before lock acquire); but we cannot proceed.
		runCancel()
		<-runErr
		t.Skip("bg process did not enter the lock-held state; cannot exercise shutdown signaling")
	}

	// Shutdown. With a 30s sleep + 100ms grace, the process MUST be
	// SIGKILLed; Shutdown must drain runWg promptly (well under 30s).
	shutdownDone := make(chan error, 1)
	start := time.Now()
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownDone <- s.Shutdown(shutdownCtx)
	}()

	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("Shutdown returned error (runWg leaked the bg waiter): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return within 5s; bg waiter did not respect runCtx cancel")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Shutdown took %v; expected sub-second after SIGKILL grace (shutdownGrace=%v)", took, s.shutdownGrace)
	}

	runCancel()
	<-runErr
}

// TestJobRunInternal_NilReleaseStillBalances guards the direct-test
// entry point (Job.Run with no scheduler bookkeeping): a nil release
// callback must not cause a nil deref no matter which switch arm runs,
// including the RunInBackground command path's failure modes.
func TestJobRunInternal_NilReleaseStillBalances(t *testing.T) {
	t.Parallel()

	// Closure path.
	j := &Job{schedule: &Schedule{}, timezone: time.Local, callback: func() {}}
	if err := j.Run(); err != nil {
		t.Fatalf("closure Run: %v", err)
	}

	// errCallback path.
	target := errors.New("err-callback boom")
	j2 := &Job{schedule: &Schedule{}, timezone: time.Local, errCallback: func() error { return target }}
	if err := j2.Run(); !errors.Is(err, target) {
		t.Fatalf("errCallback Run: expected %v, got %v", target, err)
	}

	// Synchronous command path.
	j3 := &Job{schedule: &Schedule{}, timezone: time.Local, command: "true"}
	if err := j3.Run(); err != nil && !errIsExecNotFound(err) {
		t.Fatalf("sync cmd Run: %v", err)
	}

	// RunInBackground command path: nil release means no waiter is
	// spawned (legacy behaviour). The OS process is fire-and-forget.
	if runtime.GOOS != "windows" {
		j4 := &Job{
			schedule:        &Schedule{},
			timezone:        time.Local,
			command:         "sleep",
			args:            []string{"0.05"},
			runInBackground: true,
		}
		if err := j4.Run(); err != nil {
			t.Fatalf("background cmd Run: %v", err)
		}
		// Give the spawned sleep a moment to reap; nothing for the
		// test to assert beyond "no panic".
		time.Sleep(150 * time.Millisecond)
	}
}

// errIsExecNotFound is true when the OS rejects the synthetic command;
// the test only cares that runInternal balanced its release path, not
// whether `true` is on this host's PATH.
func errIsExecNotFound(err error) bool {
	_, ok := err.(*exec.Error)
	return ok
}
