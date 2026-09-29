package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// brokenLogger panics on every call.
type brokenLogger struct{}

func (brokenLogger) Debug(string, ...any)        { panic("logger broke") }
func (brokenLogger) Info(string, ...any)         { panic("logger broke") }
func (brokenLogger) Warn(string, ...any)         { panic("logger broke") }
func (brokenLogger) Error(string, ...any)        { panic("logger broke") }
func (brokenLogger) Fatal(string, ...any)        { panic("logger broke") }
func (brokenLogger) With(...any) contract.Logger { panic("logger broke") }

// panickingLocker panics in Acquire.
type panickingLocker struct{}

func (panickingLocker) Acquire(context.Context, string, time.Duration) (Lock, error) {
	panic("locker broke")
}

// tickSurvives runs one tick on the calling goroutine, as Run's loop does,
// and fails the test when a panic escapes it: on the ticker goroutine that
// panic would kill the process.
func tickSurvives(t *testing.T, s *Scheduler) {
	t.Helper()
	var escaped any
	func() {
		defer func() { escaped = recover() }()
		s.runDueJobs()
	}()
	if escaped != nil {
		t.Fatalf("a panic escaped the tick: %v", escaped)
	}
	select {
	case <-runWgDone(s):
	case <-time.After(5 * time.Second):
		t.Fatal("the tick's runs were never released")
	}
}

// A Locker whose Acquire panics is a failed acquire: the task is skipped
// with the backend-error warning and the ticker goroutine survives.
func TestRunDueJobs_PanickingLockerIsContained(t *testing.T) {
	for _, guard := range []string{"OnOneServer", "WithoutOverlapping"} {
		t.Run(guard, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			s := New()
			s.SetLocker(panickingLocker{})
			ran := false
			j := s.Named("contain.locker."+guard, func() { ran = true }).Cron("* * * * *")
			if guard == "OnOneServer" {
				j.OnOneServer()
			} else {
				j.WithoutOverlapping()
			}
			tickSurvives(t, s)
			if ran {
				t.Error("the task ran without its lock")
			}
			if n := fallback.Count("WARN", "Skipping job: Locker.Acquire backend error"); n != 1 {
				t.Errorf("backend-error warnings = %d, want 1", n)
			}
		})
	}
}

// A scheduler-level callback that panics is reported through a logger that
// panics too: the line goes to the fallback and the ticker goroutine
// survives.
func TestRunDueJobs_PanickingCallbackLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	s := New()
	s.SetLogger(brokenLogger{})
	s.Before(func() { panic("callback broke") })
	tickSurvives(t, s)
	if n := fallback.Count("ERROR", "velocity/scheduler: scheduler-level callback panicked"); n != 1 {
		t.Errorf("fallback callback-panic lines = %d, want 1", n)
	}
}

// Run, ValidateJobs and Shutdown write their lines through a logger that
// panics without dying: Run keeps its goroutine, Shutdown still drains
// and returns, and the configuration errors reach the fallback.
func TestScheduler_PanickingLifecycleLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	s := New()
	s.SetLogger(brokenLogger{})
	s.Named("contain.lifecycle", func() {}).Cron("*/0 * * * *")

	ran := make(chan any, 1)
	go func() {
		defer func() { ran <- recover() }()
		_ = s.Run(context.Background())
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.RLock()
		running := s.running
		s.mu.RUnlock()
		if running || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if n := fallback.Wait("ERROR", "velocity/scheduler: invalid schedule configuration; job will never fire", 1, 5*time.Second); n != 1 {
		t.Errorf("fallback invalid-schedule lines = %d, want 1", n)
	}

	var escaped any
	func() {
		defer func() { escaped = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	}()
	if escaped != nil {
		t.Fatalf("a panic escaped Shutdown: %v", escaped)
	}
	select {
	case p := <-ran:
		if p != nil {
			t.Fatalf("a panic escaped Run: %v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// The manager's recovery line for a scheduler that panicked in RunAll is
// written through a logger that panics too: RunAll returns the recovered
// panic as its error instead of dying in its recovery handler.
func TestManager_PanickingRecoveryLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	m := NewManager()
	m.logger.Store(mgrLoggerHolder{Logger: brokenLogger{}})
	m.schedulers["broken"] = nil // Run on a nil scheduler panics

	errc := make(chan error, 1)
	go func() { errc <- m.RunAll(context.Background()) }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("RunAll returned nil for a scheduler that panicked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAll never returned")
	}
	if n := fallback.Count("ERROR", "velocity/scheduler: run panic recovered"); n != 1 {
		t.Errorf("fallback recovery lines = %d, want 1", n)
	}
}
