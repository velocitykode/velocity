package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// panickyLogger panics on With and on every line, like a broken redactor.
type panickyLogger struct{}

func (panickyLogger) Debug(string, ...any)        { panic("logger broke") }
func (panickyLogger) Info(string, ...any)         { panic("logger broke") }
func (panickyLogger) Warn(string, ...any)         { panic("logger broke") }
func (panickyLogger) Error(string, ...any)        { panic("logger broke") }
func (panickyLogger) Fatal(string, ...any)        { panic("logger broke") }
func (panickyLogger) With(...any) contract.Logger { panic("with broke") }

// A logger whose With (and every line) panics when a due task's run binds
// its logger does not crash the process: the run's recovery is installed
// first, and its cleanup (the in-flight count and the overlap lock) runs
// even though the diagnostic line panics too.
func TestRunDueJobs_PanickingLoggerBindingIsRecovered(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	s := New()
	s.SetLogger(panickyLogger{})
	s.Call(func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute())).WithoutOverlapping()

	s.runDueJobs()

	done := make(chan struct{})
	go func() {
		waitTicks(s)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run never released its in-flight count")
	}
	if n := fallback.Wait("ERROR", "velocity/scheduler: run due jobs panic recovered", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback lines = %d, want 1 (the logger panicked while writing)", n)
	}
}

// withOnlyPanics panics on With only; its lines are recorded.
type withOnlyPanics struct{ lines chan []any }

func (withOnlyPanics) Debug(string, ...any)         {}
func (withOnlyPanics) Info(string, ...any)          {}
func (withOnlyPanics) Warn(string, ...any)          {}
func (l withOnlyPanics) Error(_ string, kvs ...any) { l.lines <- kvs }
func (withOnlyPanics) Fatal(string, ...any)         {}
func (withOnlyPanics) With(...any) contract.Logger  { panic("with broke") }

// When only binding panics, the recovered panic is written through the
// scheduler's logger with the task name.
func TestRunDueJobs_BindingPanicLogsWithTheTaskName(t *testing.T) {
	l := withOnlyPanics{lines: make(chan []any, 4)}
	s := New()
	s.SetLogger(l)
	s.Named("nightly-report", func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))
	s.runDueJobs()
	select {
	case kvs := <-l.lines:
		if fmt.Sprint(kvs...) == "" || kvs[0] != "task_name" || kvs[1] != "nightly-report" {
			t.Errorf("line kvs = %v, want task_name=nightly-report first", kvs)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered binding panic never logged")
	}
	waitTicks(s)
}
