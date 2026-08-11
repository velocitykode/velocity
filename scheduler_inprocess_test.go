package velocity

import (
	"context"
	"os"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/velocitykode/velocity/scheduler"
)

// TestWithSchedulerInProcess_FlagWires covers the option plumbing: calling
// WithSchedulerInProcess() must flip the App.runScheduler flag during New().
// This is the lightweight unit-level assertion; integration coverage follows
// in TestWithSchedulerInProcess_RunsUnderServe.
func TestWithSchedulerInProcess_FlagWires(t *testing.T) {
	t.Run("default off", func(t *testing.T) {
		a, err := NewTestApp()
		if err != nil {
			t.Fatalf("NewTestApp() error: %v", err)
		}
		t.Cleanup(func() { _ = a.Shutdown(context.Background()) })

		if a.runScheduler {
			t.Error("runScheduler = true by default, want false (separate-process default must hold)")
		}
	})

	t.Run("opt-in flips flag", func(t *testing.T) {
		a, err := NewTestApp(WithSchedulerInProcess())
		if err != nil {
			t.Fatalf("NewTestApp() error: %v", err)
		}
		t.Cleanup(func() { _ = a.Shutdown(context.Background()) })

		if !a.runScheduler {
			t.Error("runScheduler = false after WithSchedulerInProcess(), want true")
		}
	})
}

// TestWithSchedulerInProcess_RunsUnderServe is the integration test for
// item 17. It wires a CallE-registered job that runs every minute (matching
// production usage) and a Before callback that fires unconditionally on
// each runDueJobs tick. The scheduler runs runDueJobs once immediately on
// Run() (see scheduler.Run "Run immediately on start" branch), so the
// Before callback firing is sufficient evidence that the in-process loop
// was actually started. We then send SIGTERM to trigger the existing
// graceful-shutdown path so the test exercises the real code path
// end-to-end (signal handling + Shutdown) rather than poking internals.
func TestWithSchedulerInProcess_RunsUnderServe(t *testing.T) {
	a, err := NewTestApp(
		WithSchedulerInProcess(),
		WithPort("0"), // ephemeral port so we don't collide with a real listener
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var beforeFired atomic.Int32
	a.Schedule(func(s scheduler.TaskScheduler) {
		// Add a no-op job; runDueJobs walks the whole list each tick and
		// fires the scheduler-level Before callback unconditionally, so we
		// observe the loop heartbeat even if the cron expression doesn't
		// happen to match the current minute.
		s.CallE(func() error { return nil }).EveryMinute().Name("ping")
	})

	// The scheduler.Scheduler concrete type exposes Before(); the
	// TaskScheduler interface does not. Reach through the typed app
	// service to install a tick observer.
	if sched, ok := a.Scheduler.(*scheduler.Scheduler); ok {
		sched.Before(func() { beforeFired.Add(1) })
	} else {
		t.Fatalf("a.Scheduler is not *scheduler.Scheduler: %T", a.Scheduler)
	}

	// Run serveHTTP in a goroutine; signal it to stop after we've observed
	// the scheduler heartbeat. Use a buffered errCh so the goroutine never
	// blocks on send if the test exits early.
	errCh := make(chan error, 1)
	go func() { errCh <- a.serveHTTP() }()

	// Wait for the in-process scheduler tick. runDueJobs runs immediately
	// on Run(), so this should fire well within 2s; deadline gives slack
	// for slow CI without making a flake.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if beforeFired.Load() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := beforeFired.Load(); got == 0 {
		t.Fatalf("scheduler Before callback never fired; in-process scheduler did not start")
	}

	// Send SIGTERM to trigger graceful shutdown via the same signal path
	// real consumers exercise. defer signal.Stop in serveHTTP makes this
	// safe across repeated invocations.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveHTTP returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return within 10s of SIGTERM")
	}
}

// TestWithSchedulerInProcess_OffByDefault verifies the negative path: when
// WithSchedulerInProcess is NOT set, the scheduler loop is not started
// under serveHTTP. This locks in the separate-process default so a
// future refactor can't silently flip it on (which would cause job
// duplication for consumers running a separate `vel schedule work`).
func TestWithSchedulerInProcess_OffByDefault(t *testing.T) {
	a, err := NewTestApp(WithPort("0"))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var beforeFired atomic.Int32
	if sched, ok := a.Scheduler.(*scheduler.Scheduler); ok {
		sched.Before(func() { beforeFired.Add(1) })
	} else {
		t.Fatalf("a.Scheduler is not *scheduler.Scheduler: %T", a.Scheduler)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- a.serveHTTP() }()

	// Give the server time to bind and the scheduler a chance to be
	// (incorrectly) started. 250ms is well under the 1-minute scheduler
	// tick but long enough to observe the immediate-on-start runDueJobs
	// call if it were running.
	time.Sleep(250 * time.Millisecond)

	if got := beforeFired.Load(); got != 0 {
		t.Errorf("scheduler Before fired %d times; expected 0 (default off)", got)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("syscall.Kill: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveHTTP returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveHTTP did not return within 10s of SIGTERM")
	}
}

// TestWithSchedulerInProcess_ShutdownDrainsScheduler verifies that
// App.Shutdown stops the in-process scheduler and waits for in-flight
// jobs. We register a job that blocks on a channel; once Shutdown is
// invoked, the job must be allowed to drain (we close the channel) and
// Shutdown must return cleanly.
func TestWithSchedulerInProcess_ShutdownDrainsScheduler(t *testing.T) {
	a, err := NewTestApp(WithSchedulerInProcess(), WithPort("0"))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	release := make(chan struct{})
	jobStarted := make(chan struct{})
	var jobReturned atomic.Bool

	a.Schedule(func(s scheduler.TaskScheduler) {
		s.CallE(func() error {
			close(jobStarted)
			<-release
			jobReturned.Store(true)
			return nil
		}).Cron("* * * * *").Name("blocking-job")
	})

	errCh := make(chan error, 1)
	go func() { errCh <- a.serveHTTP() }()

	// Wait for the job to start (proves runDueJobs picked it up).
	select {
	case <-jobStarted:
	case <-time.After(2 * time.Second):
		// Make sure we don't deadlock the goroutine on shutdown if the
		// scheduler never picked the job up.
		close(release)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-errCh
		t.Fatal("blocking job never started; in-process scheduler did not run")
	}

	// Trigger shutdown in a goroutine, release the job, and verify Shutdown
	// returned cleanly with the job drained. We use SIGTERM to drive the
	// real graceful-shutdown path.
	go func() {
		// Give the signal handler a moment to wire up before sending.
		time.Sleep(20 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		// Release the job slightly after SIGTERM so Shutdown actually
		// has to wait on runWg.
		time.Sleep(20 * time.Millisecond)
		close(release)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("serveHTTP returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		close(release)
		t.Fatal("serveHTTP did not return within 15s of SIGTERM")
	}

	if !jobReturned.Load() {
		t.Error("blocking job did not run to completion before shutdown")
	}
}
