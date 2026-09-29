package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// blockedTask runs s with one task that blocks until the returned
// release is closed, and waits until the task runs.
func blockedTask(t *testing.T, s *Scheduler) (release chan struct{}, runDone chan error) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s.Named("drain.blocked", func() {
		once.Do(func() { close(entered) })
		<-release
	}).Cron("* * * * *")
	runDone = make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("task never ran")
	}
	return release, runDone
}

// timedOutShutdown shuts s down with a deadline shorter than its task.
func timedOutShutdown(t *testing.T, s *Scheduler) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown = %v, want its deadline", err)
	}
}

// A Shutdown after one that timed out waits for the task that one
// admitted, and returns nil only once it has finished.
func TestShutdown_AfterATimedOutOneWaitsForTheDrain(t *testing.T) {
	s := New()
	release, _ := blockedTask(t, s)
	timedOutShutdown(t, s)

	second := make(chan error, 1)
	go func() { second <- s.Shutdown(context.Background()) }()
	select {
	case err := <-second:
		t.Fatalf("second Shutdown returned %v while the admitted task still ran", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-second:
		if err != nil {
			t.Errorf("second Shutdown = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Shutdown did not return after the task ended")
	}
}

// That second Shutdown returns its own deadline when the task outlives it.
func TestShutdown_AfterATimedOutOneReturnsItsDeadline(t *testing.T) {
	s := New()
	release, _ := blockedTask(t, s)
	defer close(release)
	timedOutShutdown(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second Shutdown = %v, want its deadline", err)
	}
}

// startCounter counts the scheduler's started lines.
type startCounter struct{ n atomic.Int32 }

func (c *startCounter) Info(msg string, _ ...any) {
	if msg == "Scheduler started" {
		c.n.Add(1)
	}
}
func (c *startCounter) Debug(string, ...any)        {}
func (c *startCounter) Warn(string, ...any)         {}
func (c *startCounter) Error(string, ...any)        {}
func (c *startCounter) Fatal(string, ...any)        {}
func (c *startCounter) With(...any) contract.Logger { return c }

// A Run while a timed-out Shutdown's drain is pending does not start
// until that drain has ended.
func TestRun_DoesNotOvertakeAPendingDrain(t *testing.T) {
	s := New()
	starts := &startCounter{}
	s.SetLogger(starts)
	release, _ := blockedTask(t, s)
	timedOutShutdown(t, s)

	rerun := make(chan error, 1)
	go func() { rerun <- s.Run(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if got := starts.n.Load(); got != 1 {
		t.Fatalf("started %d times while the drain was pending, want 1", got)
	}
	close(release)
	deadline := time.After(2 * time.Second)
	for starts.n.Load() != 2 {
		select {
		case <-deadline:
			t.Fatal("Run did not start once the drain ended")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-rerun
}

// A Run called from the task a pending drain waits on would wait on
// itself: it returns an error wrapping contract.ErrStopFromOwnWork at once.
func TestRun_FromTheDrainedTaskIsRefused(t *testing.T) {
	s := New()
	entered, release := make(chan struct{}), make(chan struct{})
	runErr := make(chan error, 1)
	var once sync.Once
	s.Named("drain.self", func() {
		first := false
		once.Do(func() { first = true })
		if !first {
			return
		}
		close(entered)
		<-release
		runErr <- s.Run(context.Background())
	}).Cron("* * * * *")
	go func() { _ = s.Run(context.Background()) }()
	<-entered
	timedOutShutdown(t, s)
	close(release)
	select {
	case err := <-runErr:
		if !errors.Is(err, contract.ErrStopFromOwnWork) {
			t.Errorf("Run from the drained task = %v, want an error wrapping contract.ErrStopFromOwnWork", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run from the drained task waited on itself")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown after the task = %v", err)
	}
}

// blockingLineLogger blocks on its shutting-down line until release.
type blockingLineLogger struct{ release chan struct{} }

func (l *blockingLineLogger) Info(msg string, _ ...any) {
	if strings.Contains(msg, "shutting down") {
		<-l.release
	}
}
func (l *blockingLineLogger) Debug(string, ...any)        {}
func (l *blockingLineLogger) Warn(string, ...any)         {}
func (l *blockingLineLogger) Error(string, ...any)        {}
func (l *blockingLineLogger) Fatal(string, ...any)        {}
func (l *blockingLineLogger) With(...any) contract.Logger { return l }

// A shutting-down line that blocks does not hold Shutdown past its ctx.
func TestShutdown_BlockingLineDoesNotHoldTheDeadline(t *testing.T) {
	s := New()
	logger := &blockingLineLogger{release: make(chan struct{})}
	defer close(logger.release)
	s.SetLogger(logger)
	s.Call(func() {}).EveryMinute()
	go func() { _ = s.Run(context.Background()) }()
	waitRunning(t, s, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Shutdown(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown = %v, want its deadline", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a blocking line held Shutdown past its deadline")
	}
}
