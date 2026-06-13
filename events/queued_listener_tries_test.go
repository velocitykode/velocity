package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// silentWorkerLogger is a no-op queue.WorkerLogger so the worker's retry /
// failure lines do not spam the test output.
type silentWorkerLogger struct{}

func (silentWorkerLogger) Info(string, ...any)  {}
func (silentWorkerLogger) Warn(string, ...any)  {}
func (silentWorkerLogger) Error(string, ...any) {}

// failingTriesListener is a QueuedListener whose Handle always errors. The
// atomic counter records how many times the worker invoked it so the test can
// assert the retry budget derived from Tries() is honoured exactly.
type failingTriesListener struct {
	tries int
	calls *atomic.Int32
}

func (l *failingTriesListener) Handle(ctx context.Context, event interface{}) error {
	l.calls.Add(1)
	return errors.New("velocity/events: forced queued-listener failure")
}

func (l *failingTriesListener) ShouldQueue() bool        { return true }
func (l *failingTriesListener) OnConnection() string     { return "default" }
func (l *failingTriesListener) OnQueue() string          { return "" } // -> "default"
func (l *failingTriesListener) WithDelay() time.Duration { return 0 }
func (l *failingTriesListener) Tries() int               { return l.tries }

// runQueuedListenerTries dispatches a scalar event to a queued listener whose
// Handle always fails, runs a real queue.Worker against the memory driver
// until the job lands in failed_jobs, and returns the number of Handle
// invocations plus the failed-job count for "default".
//
// The worker is configured with WithMaxRetries(workerDefault) deliberately
// different from tries: the assertion that calls == tries (not workerDefault)
// is what pins the regression. EventListenerJob.MaxAttempts() must win over
// the worker default, which only happens because the job implements
// queue.MaxAttempter.
func runQueuedListenerTries(t *testing.T, tries, workerDefault int) (calls int, failedCount int) {
	t.Helper()

	driver := queue.NewMemoryDriver()
	driver.Start()
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	counter := &atomic.Int32{}
	listener := &failingTriesListener{tries: tries, calls: counter}

	dispatcher := NewQueueIntegratedDispatcher()
	dispatcher.SetQueueDriver(driver)

	// The worker rehydrates the listener from the package-level factory
	// registry (the dispatcher does not set the live listener pointer in
	// pushToQueue), so the factory must be registered before Dispatch.
	listenerType := dispatcher.getListenerType(listener)
	dispatcher.RegisterListenerFactory(listenerType, func() Listener { return listener })
	t.Cleanup(func() { UnregisterListenerFactory(listenerType) })

	const eventName = "queued.tries.test"
	dispatcher.Listen(eventName, listener)

	handler := func(j queue.Job) error { return j.Handle() }
	worker := queue.NewWorker(driver, "default", handler,
		queue.WithMaxRetries(workerDefault),
		queue.WithBackoff(queue.FixedBackoff(0)),
		queue.WithInterval(2*time.Millisecond),
		queue.WithWorkerLogger(silentWorkerLogger{}),
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); worker.Start(context.Background()) }()

	// A scalar string event needs no RegisterEventFactory (the hydration
	// path has a built-in scalar shortcut), keeping the test focused on the
	// retry-budget contract rather than event-factory wiring.
	if err := dispatcher.Dispatch(context.Background(), eventName); err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	// The memory driver promotes a released retry job back onto the main
	// queue only on its 1s background ticker, so each retry costs ~1s; a
	// Tries()=5 run needs roughly four ticks. Budget generously so a slow
	// CI box does not clip the final attempt.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		failed, _ := driver.GetFailed("default")
		if len(failed) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	worker.Stop()
	wg.Wait()

	failed, _ := driver.GetFailed("default")
	return int(counter.Load()), len(failed)
}

// TestQueuedListenerTriesHonoured pins the regression: the queue worker must
// derive a queued listener's retry budget from QueuedListener.Tries() (carried
// onto EventListenerJob.MaxRetries and surfaced via MaxAttempts()), not from
// the worker's package default. Before EventListenerJob implemented
// queue.MaxAttempter every queued listener silently inherited the worker
// default regardless of Tries().
func TestQueuedListenerTriesHonoured(t *testing.T) {
	// Tries()=1 with a worker default of 3: the listener must be attempted
	// exactly once and then land in failed_jobs. Pre-fix it would have
	// retried up to the worker default.
	t.Run("SingleTry", func(t *testing.T) {
		calls, failed := runQueuedListenerTries(t, 1, 3)
		if calls != 1 {
			t.Fatalf("expected listener attempted exactly once (Tries=1), got %d", calls)
		}
		if failed != 1 {
			t.Fatalf("expected exactly 1 failed_jobs entry, got %d", failed)
		}
	})

	// Tries()=5 with a worker default of 3: the job's larger budget must win,
	// so the listener is attempted 5 times before terminal failure.
	t.Run("FiveTries", func(t *testing.T) {
		calls, failed := runQueuedListenerTries(t, 5, 3)
		if calls != 5 {
			t.Fatalf("expected listener attempted 5 times (Tries=5), got %d", calls)
		}
		if failed != 1 {
			t.Fatalf("expected exactly 1 failed_jobs entry, got %d", failed)
		}
	})
}
