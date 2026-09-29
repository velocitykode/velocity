package scheduler

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// When and Skip run without the job's lock: one that calls a setter on
// its own job does not deadlock the tick.
func TestShouldRun_ConditionMayCallTheJobsSetters(t *testing.T) {
	for _, kind := range []string{"When", "Skip"} {
		t.Run(kind, func(t *testing.T) {
			s := New()
			var ran atomic.Bool
			j := s.Named("condition.setter."+kind, func() { ran.Store(true) }).Cron("* * * * *")
			if kind == "When" {
				j.When(func() bool { j.EvenInMaintenanceMode(); return true })
			} else {
				j.Skip(func() bool { j.EvenInMaintenanceMode(); return false })
			}
			done := make(chan struct{})
			go func() { defer close(done); s.runDueJobs() }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("the tick deadlocked on a %s callback that calls a job setter", kind)
			}
			s.runWg.Wait()
			if !ran.Load() {
				t.Errorf("the task did not run after its %s callback allowed it", kind)
			}
		})
	}
}

// A When or Skip callback that panics skips the run, is logged, and does
// not kill the ticker goroutine.
func TestShouldRun_PanickingConditionIsContained(t *testing.T) {
	for _, kind := range []string{"When", "Skip"} {
		t.Run(kind, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			s := New()
			var ran atomic.Bool
			j := s.Named("condition.panic."+kind, func() { ran.Store(true) }).Cron("* * * * *")
			broken := func() bool { panic("condition broke") }
			if kind == "When" {
				j.When(broken)
			} else {
				j.Skip(broken)
			}
			tickSurvives(t, s)
			if ran.Load() {
				t.Errorf("the task ran although its %s callback panicked", kind)
			}
			if n := fallback.Count("ERROR", "velocity/scheduler: task condition panicked; the task does not run"); n != 1 {
				t.Errorf("condition-panic lines = %d, want 1", n)
			}
		})
	}
}

// A direct ShouldRun on a job with no scheduler contains the panic too.
func TestShouldRun_PanickingConditionWithoutASchedulerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	j := &Job{name: "condition.alone", schedule: &Schedule{}, timezone: time.UTC}
	j.When(func() bool { panic("condition broke") })
	if j.ShouldRun() {
		t.Error("ShouldRun reported true for a panicking When")
	}
	if n := fallback.Count("ERROR", "velocity/scheduler: task condition panicked; the task does not run"); n != 1 {
		t.Errorf("condition-panic lines = %d, want 1", n)
	}
}

// ShouldRun's snapshot races the job's setters from many goroutines: run
// under -race.
func TestShouldRun_ConcurrentWithSetters(t *testing.T) {
	s := New()
	j := s.Named("condition.race", func() {}).Cron("* * * * *")
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for range 200 {
				if i%2 == 0 {
					_ = j.ShouldRun()
				} else {
					j.When(func() bool { return true }).Skip(func() bool { return false }).Environments("test").Between("00:00", "23:59")
				}
			}
		})
	}
	wg.Wait()
}
