package scheduler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// gatedLogger panics on With, and holds each Warn and Error line until
// the test opens its gate (or panics, when panics is set). entered
// receives once per held line.
type gatedLogger struct {
	entered chan struct{}
	gate    chan struct{}
	panics  bool
}

func newGatedLogger() *gatedLogger {
	return &gatedLogger{entered: make(chan struct{}, 8), gate: make(chan struct{})}
}

func (l *gatedLogger) hold() {
	l.entered <- struct{}{}
	if l.panics {
		panic("diagnostic broke")
	}
	<-l.gate
}

func (*gatedLogger) Debug(string, ...any)        {}
func (*gatedLogger) Info(string, ...any)         {}
func (l *gatedLogger) Warn(string, ...any)       { l.hold() }
func (l *gatedLogger) Error(string, ...any)      { l.hold() }
func (*gatedLogger) Fatal(string, ...any)        {}
func (*gatedLogger) With(...any) contract.Logger { panic("with broke") }

// runWgDone returns a channel closed once s has no run in flight.
func runWgDone(s *Scheduler) <-chan struct{} {
	done := make(chan struct{})
	go func() { s.runWg.Wait(); close(done) }()
	return done
}

// assertStillCounted fails when s's in-flight count drains while a
// diagnostic is still being written: a Shutdown waiting on it would
// return, and the app would close the logger under the line.
func assertStillCounted(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		t.Fatalf("the run stopped being counted while its %s was still being written", what)
	case <-time.After(100 * time.Millisecond):
	}
}

// A due task whose logger binding panics stays counted until its panic
// diagnostic has been written, so Shutdown cannot overtake the line.
func TestRunDueJobs_PanicDiagnosticKeepsTheRunCounted(t *testing.T) {
	l := newGatedLogger()
	s := New()
	s.SetLogger(l)
	s.Named("drain.panic", func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))

	s.runDueJobs()
	select {
	case <-l.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("panic diagnostic never written")
	}
	done := runWgDone(s)
	assertStillCounted(t, done, "panic diagnostic")
	close(l.gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run never released after its diagnostic finished")
	}
}

// A panic diagnostic whose logger panics still releases the run, and the
// line goes to the fallback.
func TestRunDueJobs_PanickingPanicDiagnosticStillReleases(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	l := newGatedLogger()
	l.panics = true
	s := New()
	s.SetLogger(l)
	s.Named("drain.panic2", func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))

	s.runDueJobs()
	select {
	case <-runWgDone(s):
	case <-time.After(2 * time.Second):
		t.Fatal("run never released after its diagnostic panicked")
	}
	if n := fallback.Wait("ERROR", "velocity/scheduler: run due jobs panic recovered", 1, 2*time.Second); n != 1 {
		t.Errorf("fallback lines = %d, want 1", n)
	}
}

// selfLogger binds by returning itself and holds each Error line like
// gatedLogger, so a panic past the binding reaches the run's recovery.
type selfLogger struct{ *gatedLogger }

func (l selfLogger) With(...any) contract.Logger { return l }

// A panic raised inside the run itself (here, an event dispatcher that
// panics on the task's started event) keeps the run counted until the
// run's recovery has written the panic line.
func TestRunDueJobs_PanicInsideTheRunKeepsItCountedUntilLogged(t *testing.T) {
	l := selfLogger{newGatedLogger()}
	s := New()
	s.SetLogger(l)
	s.SetEventDispatcher(func(context.Context, interface{}) error { panic("dispatcher broke") })
	s.Named("drain.inner", func() {}).Cron(fmt.Sprintf("%d * * * *", time.Now().Minute()))

	s.runDueJobs()
	select {
	case <-l.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("panic diagnostic never written")
	}
	done := runWgDone(s)
	assertStillCounted(t, done, "panic diagnostic")
	close(l.gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run never released after its diagnostic finished")
	}
}
