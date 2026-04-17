package scheduler

import (
	"sync"
	"testing"
	"time"
)

func TestJob(t *testing.T) {
	t.Run("FluentAPI", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}}

		job.EveryMinute()
		if job.schedule.minute != "*" {
			t.Error("expected every minute")
		}

		job.Daily()
		if job.schedule.hour != "0" || job.schedule.minute != "0" {
			t.Error("expected daily at midnight")
		}

		job.DailyAt("14:30")
		if job.schedule.hour != "14" || job.schedule.minute != "30" {
			t.Error("expected daily at 14:30")
		}

		job.Weekly()
		if job.schedule.dayOfWeek != "0" {
			t.Error("expected weekly on Sunday")
		}

		job.Weekdays()
		if job.schedule.dayOfWeek != "1-5" {
			t.Error("expected weekdays")
		}

		job.Mondays()
		if job.schedule.dayOfWeek != "1" {
			t.Error("expected Mondays")
		}
	})

	t.Run("WithoutOverlapping", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}}
		job.WithoutOverlapping()

		if !job.withoutOverlapping {
			t.Error("expected withoutOverlapping to be true")
		}
		if job.mutex == nil {
			t.Error("expected mutex to be initialized")
		}
	})

	t.Run("Constraints", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}, timezone: time.Local}

		// Test When constraint
		job.When(func() bool { return false })
		if job.ShouldRun() {
			t.Error("expected job not to run when condition is false")
		}

		job.When(func() bool { return true })
		if !job.ShouldRun() {
			t.Error("expected job to run when condition is true")
		}

		// Test Skip constraint
		job.Skip(func() bool { return true })
		if job.ShouldRun() {
			t.Error("expected job not to run when skip is true")
		}

		job.Skip(func() bool { return false })
		if !job.ShouldRun() {
			t.Error("expected job to run when skip is false")
		}

		// Test Environment constraint
		sched := New()
		sched.SetEnv("production")
		job.scheduler = sched
		job.Environments("development", "staging")
		if job.ShouldRun() {
			t.Error("expected job not to run in production")
		}

		job.Environments("production", "staging")
		if !job.ShouldRun() {
			t.Error("expected job to run in production")
		}
	})

	t.Run("Between", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}, timezone: time.Local}

		// Set time constraint that excludes current time
		now := time.Now()
		hourAgo := now.Add(-1 * time.Hour).Format("15:04")
		twoHoursAgo := now.Add(-2 * time.Hour).Format("15:04")

		job.Between(twoHoursAgo, hourAgo)
		if job.ShouldRun() {
			t.Error("expected job not to run outside time range")
		}

		// Set time constraint that includes current time
		hourLater := now.Add(1 * time.Hour).Format("15:04")
		job.Between(hourAgo, hourLater)
		if !job.ShouldRun() {
			t.Error("expected job to run within time range")
		}
	})

	t.Run("JobExecution", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}, timezone: time.Local}
		executed := false

		job.callback = func() {
			executed = true
		}

		err := job.Run()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !executed {
			t.Error("expected callback to be executed")
		}
	})

	t.Run("Hooks", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}, timezone: time.Local}
		var beforeCalled, afterCalled, successCalled bool

		job.Before(func() {
			beforeCalled = true
		})

		job.After(func() {
			afterCalled = true
		})

		job.OnSuccess(func() {
			successCalled = true
		})

		job.callback = func() {}

		job.Run()

		if !beforeCalled {
			t.Error("expected before hook to be called")
		}
		if !afterCalled {
			t.Error("expected after hook to be called")
		}
		if !successCalled {
			t.Error("expected success hook to be called")
		}
	})

	t.Run("OnFailure", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}, timezone: time.Local}
		var failureCalled bool
		var capturedError error

		job.OnFailure(func(err error) {
			failureCalled = true
			capturedError = err
		})

		job.callback = func() {
			panic("test panic")
		}

		job.Run()

		if !failureCalled {
			t.Error("expected failure hook to be called")
		}
		if capturedError == nil {
			t.Error("expected error to be captured")
		}
	})

	t.Run("PreventOverlapping", func(t *testing.T) {
		job := &Job{
			schedule:           &Schedule{},
			timezone:           time.Local,
			withoutOverlapping: true,
			mutex:              &sync.Mutex{},
		}

		executed := 0
		var mu sync.Mutex

		job.callback = func() {
			mu.Lock()
			executed++
			mu.Unlock()
			time.Sleep(100 * time.Millisecond)
		}

		// Start first execution
		go job.Run()
		time.Sleep(10 * time.Millisecond)

		// Try to run again while first is still running
		err := job.Run()
		if err == nil {
			t.Error("expected error when job is already running")
		}

		// Wait for first to complete
		time.Sleep(150 * time.Millisecond)

		mu.Lock()
		if executed != 1 {
			t.Errorf("expected 1 execution, got %d", executed)
		}
		mu.Unlock()
	})

	t.Run("Name", func(t *testing.T) {
		job := &Job{schedule: &Schedule{}}
		job.Name("test-job")
		if job.GetName() != "test-job" {
			t.Error("expected name to be 'test-job'")
		}
	})

	t.Run("CommandExecution", func(t *testing.T) {
		job := &Job{
			schedule: &Schedule{},
			timezone: time.Local,
			command:  "echo",
			args:     []string{"test"},
		}

		err := job.Run()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("RunInBackground", func(t *testing.T) {
		job := &Job{
			schedule:        &Schedule{},
			timezone:        time.Local,
			command:         "sleep",
			args:            []string{"0.1"},
			runInBackground: true,
		}

		start := time.Now()
		err := job.Run()
		duration := time.Since(start)

		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		// Background job should return immediately
		if duration > 50*time.Millisecond {
			t.Error("expected background job to return immediately")
		}
	})
}
