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

func TestScheduler(t *testing.T) {
	t.Run("NewScheduler", func(t *testing.T) {
		s := New()
		if s == nil {
			t.Fatal("expected non-nil scheduler")
		}
		if s.timezone != time.Local {
			t.Error("expected local timezone")
		}
		if len(s.jobs) != 0 {
			t.Error("expected empty jobs")
		}
	})

	t.Run("AddJob", func(t *testing.T) {
		s := New()
		job := s.Call(func() {})
		if len(s.jobs) != 1 {
			t.Error("expected 1 job")
		}
		if s.jobs[0] != job {
			t.Error("expected same job instance")
		}
	})

	t.Run("Command", func(t *testing.T) {
		s := New()
		job := s.Command("echo", "test")
		if job.command != "echo" {
			t.Error("expected command to be 'echo'")
		}
		if len(job.args) != 1 || job.args[0] != "test" {
			t.Error("expected args to be ['test']")
		}
	})

	t.Run("SetTimezone", func(t *testing.T) {
		s := New()
		tz, _ := time.LoadLocation("UTC")
		s.SetTimezone(tz)
		if s.timezone != tz {
			t.Error("expected UTC timezone")
		}
	})

	t.Run("MaintenanceMode", func(t *testing.T) {
		s := New()
		s.MaintenanceMode(true)
		if !s.maintenanceMode {
			t.Error("expected maintenance mode to be true")
		}
		s.MaintenanceMode(false)
		if s.maintenanceMode {
			t.Error("expected maintenance mode to be false")
		}
	})

	t.Run("RunAndStop", func(t *testing.T) {
		s := New()
		ctx, cancel := context.WithCancel(context.Background())

		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		err := s.Run(ctx)
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("JobExecution", func(t *testing.T) {
		s := New()
		var executed int32

		// Add a job that runs at a specific minute
		now := time.Now()
		s.Call(func() {
			atomic.AddInt32(&executed, 1)
		}).Cron(fmt.Sprintf("%d * * * *", now.Minute()))

		// Manually trigger job execution
		s.runDueJobs()

		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&executed) }, int32(1), time.Second, "job executed once")
	})

	t.Run("BeforeAfterCallbacks", func(t *testing.T) {
		s := New()
		var beforeCalled, afterCalled atomic.Bool

		s.Before(func() {
			beforeCalled.Store(true)
		})

		s.After(func() {
			afterCalled.Store(true)
		})

		s.Call(func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))
		s.runDueJobs()

		testsync.Eventually(t, func() bool { return beforeCalled.Load() && afterCalled.Load() }, time.Second, "before+after callbacks")
	})

	t.Run("MaintenanceModePreventExecution", func(t *testing.T) {
		s := New()
		var executed atomic.Bool

		s.MaintenanceMode(true)
		s.Call(func() {
			executed.Store(true)
		}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))

		s.runDueJobs()
		// Negative assertion: give any scheduled goroutine a chance to race
		// against the maintenance gate before we sample.
		time.Sleep(50 * time.Millisecond)

		if executed.Load() {
			t.Error("expected job not to execute in maintenance mode")
		}
	})

	t.Run("ConcurrentJobExecution", func(t *testing.T) {
		s := New()
		var counter int32
		var wg sync.WaitGroup

		// Add multiple jobs
		for i := 0; i < 5; i++ {
			wg.Add(1)
			s.Call(func() {
				defer wg.Done()
				atomic.AddInt32(&counter, 1)
				time.Sleep(10 * time.Millisecond)
			}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))
		}

		s.runDueJobs()
		wg.Wait()

		if atomic.LoadInt32(&counter) != 5 {
			t.Errorf("expected 5 executions, got %d", counter)
		}
	})
}
