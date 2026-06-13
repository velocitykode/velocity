package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// TestScheduler_BeforeCallbackPanic_DoesNotKillScheduler covers B42: the
// scheduler-level Before/After callbacks run on the ticker goroutine, so a
// bare panic in one used to tear down the whole scheduler. The callback is
// now isolated; a panicking Before hook must not stop due jobs from running
// or crash runDueJobs.
func TestScheduler_BeforeCallbackPanic_DoesNotKillScheduler(t *testing.T) {
	s := New()

	var jobRan, afterRan atomic.Bool
	s.Before(func() { panic("before boom") })
	s.After(func() { afterRan.Store(true) })
	s.Call(func() { jobRan.Store(true) }).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))

	// If the panic were not isolated, this synchronous call would panic out
	// of the test goroutine and fail the test.
	s.runDueJobs()

	testsync.Eventually(t, func() bool {
		return jobRan.Load() && afterRan.Load()
	}, time.Second, "job ran and After callback fired despite a panicking Before callback")
}

// TestScheduler_BackgroundWaiter_HookPanicIsolated covers the waiter gap:
// spawnBackgroundWaiter ran After/OnSuccess/OnFailure callbacks bare, so the
// first panicking hook aborted the remaining hooks and the completion event.
// Per-hook isolation must let OnSuccess still run and ScheduledTaskFinished
// still dispatch when an After callback panics on the background path.
func TestScheduler_BackgroundWaiter_HookPanicIsolated(t *testing.T) {
	s := New()

	var mu sync.Mutex
	var finished int
	s.SetEventDispatcher(func(_ context.Context, e interface{}) error {
		if _, ok := e.(*ScheduledTaskFinished); ok {
			mu.Lock()
			finished++
			mu.Unlock()
		}
		return nil
	})

	var onSuccessRan atomic.Bool
	job := s.Command("sleep", "0.05").RunInBackground().Name("bg-job")
	job.After(func() { panic("after boom") })
	job.OnSuccess(func() { onSuccessRan.Store(true) })

	// Background command returns immediately; the waiter goroutine runs the
	// hooks and completion event after cmd.Wait.
	if err := job.Run(); err != nil {
		t.Fatalf("background Run returned error: %v", err)
	}

	testsync.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return onSuccessRan.Load() && finished == 1
	}, 2*time.Second, "OnSuccess ran and ScheduledTaskFinished dispatched despite a panicking After callback")
}

// TestScheduler_ShouldRunEnvRace covers the ShouldRun data race: ShouldRun
// reads the app environment while holding Job.mu, and SetEnv used to write it
// under Scheduler.mu, a different lock, so the field was raced. appEnv is now
// an atomic pointer read lock-free. Run under -race to catch a regression.
func TestScheduler_ShouldRunEnvRace(t *testing.T) {
	s := New()
	job := s.Call(func() {}).Name("env-job")
	job.Environments("production")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		envs := []string{"production", "staging", "  PRODUCTION  "}
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				s.SetEnv(envs[i%len(envs)])
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = job.ShouldRun()
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}
