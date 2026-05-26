package events

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// h22Listener is a queued listener registered via the package factory
// registry. The Handle implementation records every invocation so the test
// can assert the listener actually ran end-to-end through the queue worker.
type h22Listener struct {
	counter *atomic.Int32
	done    chan struct{}
}

func (l *h22Listener) Handle(ctx context.Context, event interface{}) error {
	if l.counter != nil {
		l.counter.Add(1)
	}
	if l.done != nil {
		select {
		case l.done <- struct{}{}:
		default:
		}
	}
	return nil
}
func (l *h22Listener) ShouldQueue() bool { return true }

// h22FailingListener returns an error from Handle so the worker exhausts
// retries and calls EventListenerJob.Failed, exercising the reporter
// callback.
type h22FailingListener struct{}

func (h22FailingListener) Handle(ctx context.Context, event interface{}) error {
	return errors.New("listener intentional failure")
}
func (h22FailingListener) ShouldQueue() bool { return true }

// runWorkerOnce drains at most one successful job from the queue, returning
// once the listener has fired or the timeout elapses. We do not need the
// full worker lifecycle for the happy-path assertion; pulling via the
// driver directly is sufficient and avoids the worker's exponential
// backoff timing.
func runWorkerOnce(t *testing.T, drv queue.Driver, queueName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	job, err := drv.PopCtx(ctx, queueName)
	if err != nil {
		t.Fatalf("PopCtx: %v", err)
	}
	if job == nil {
		t.Fatal("no job available to drain")
	}

	// Use the HandleCtxer optional interface (matches what the real worker
	// does in queue.Worker.processJob).
	if hc, ok := job.(queue.HandleCtxer); ok {
		if err := hc.HandleCtx(ctx); err != nil {
			t.Fatalf("HandleCtx: %v", err)
		}
		return
	}
	if err := job.Handle(); err != nil {
		t.Fatalf("Handle: %v", err)
	}
}

// TestEventListenerJob_QueuedListenerEndToEnd is the H-22 regression test.
// It reproduces the previously-silent-drop flow:
//
//  1. Initialize the queue integration via InitializeQueueIntegration so the
//     queue.RegisterJob factory and the failure reporter are wired.
//  2. Register the package-level listener factory for the listener type.
//  3. Dispatch the event via QueueIntegratedDispatcher.Dispatch, which
//     pushes an EventListenerJob to the memory queue.
//  4. Drain the queue once via the driver, hydrate the job, and call
//     HandleCtx.
//  5. Assert the listener was actually invoked.
//
// Before the H-22 fix, HandleCtx returned "listener not set" because
// EventJobFactory dropped j.listener on the floor. The test would observe a
// zero counter.
func TestEventListenerJob_QueuedListenerEndToEnd(t *testing.T) {
	// Reset package-level state to keep tests isolated.
	defer UnregisterListenerFactory("events.h22Listener")
	defer setFailureReporter(nil)

	counter := &atomic.Int32{}
	done := make(chan struct{}, 1)

	// Register factory + initialize integration.
	RegisterListenerFactory("events.h22Listener", func() Listener {
		return &h22Listener{counter: counter, done: done}
	})

	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())

	d := NewQueueIntegratedDispatcher()
	InitializeQueueIntegration(d, drv, nil)
	// Register the listener at the dispatcher level too so Dispatch finds it.
	d.Listen("h22.event", &h22Listener{counter: counter, done: done})

	if err := d.Dispatch(context.Background(), "h22.event"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// At this point one EventListenerJob lives in the memory queue. Drain it.
	runWorkerOnce(t, drv, "default")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("listener was not invoked end-to-end through the queue worker")
	}

	if got := counter.Load(); got != 1 {
		t.Fatalf("listener invocation count: got %d want 1", got)
	}
}

// TestEventListenerJob_FailedReportsThroughReporter is the H-22 Failed-path
// test. It asserts that EventListenerJob.Failed routes through the
// FailureReporter installed via InitializeQueueIntegration, instead of
// silently dropping the failure as the original no-op did.
func TestEventListenerJob_FailedReportsThroughReporter(t *testing.T) {
	defer UnregisterListenerFactory("events.h22FailingListener")
	defer setFailureReporter(nil)

	var reported atomic.Int32
	var lastErr atomic.Pointer[error]
	reporter := func(job *EventListenerJob, err error) {
		reported.Add(1)
		lastErr.Store(&err)
	}

	RegisterListenerFactory("events.h22FailingListener", func() Listener { return h22FailingListener{} })
	InitializeQueueIntegration(nil, nil, reporter)

	job := &EventListenerJob{
		Event:        "h22.failure",
		EventType:    "h22.failure",
		ListenerType: "events.h22FailingListener",
		MaxRetries:   1,
	}

	// Simulate the queue driver path: Handle returns the listener error,
	// then the driver calls job.Failed(err) when retries are exhausted.
	handleErr := job.Handle()
	if handleErr == nil {
		t.Fatal("expected error from failing listener")
	}
	job.Failed(handleErr)

	if got := reported.Load(); got != 1 {
		t.Fatalf("reporter invocation count: got %d want 1", got)
	}
	if ptr := lastErr.Load(); ptr == nil || *ptr == nil || (*ptr).Error() != handleErr.Error() {
		t.Fatalf("reporter received wrong error: %v", lastErr.Load())
	}
}

// TestEventListenerJob_FailedNoReporterIsNoop verifies that when no reporter
// is installed (default state, e.g. tests that exercise the queue path
// without InitializeQueueIntegration), Failed does not panic and does not
// dispatch anywhere. The test exists to lock in the contract: silent drop
// is no longer the default, but the call must still be safe when no
// reporter is registered.
func TestEventListenerJob_FailedNoReporterIsNoop(t *testing.T) {
	defer setFailureReporter(nil)
	setFailureReporter(nil)

	job := &EventListenerJob{ListenerType: "events.h22FailingListener"}
	// Must not panic.
	job.Failed(errors.New("dropped"))
}

// TestPushToQueue_RefusesWithoutFactory locks in the dispatch-side guard
// added by H-22: pushToQueue refuses to enqueue when the listener type has
// no factory registered, so a queued security listener cannot land on the
// queue with no possible hydration path.
func TestPushToQueue_RefusesWithoutFactory(t *testing.T) {
	d := NewQueueIntegratedDispatcher()
	// Build a real memory queue but never register the listener factory.
	drv := queue.NewMemoryDriver()
	defer drv.Shutdown(context.Background())
	d.SetQueueDriver(drv)

	// Use a listener type that is guaranteed not to be in the registry.
	UnregisterListenerFactory("events.h22FailingListener")

	d.Listen("guarded.event", h22FailingListener{})
	err := d.Dispatch(context.Background(), "guarded.event")
	if err == nil {
		t.Fatal("expected Dispatch to refuse: no factory registered")
	}
	if !errors.Is(err, ErrListenerNotFound) {
		t.Fatalf("expected ErrListenerNotFound, got %v", err)
	}

	// Confirm nothing landed on the queue.
	if size, _ := drv.Size("default"); size != 0 {
		t.Fatalf("queue size after refused push: got %d want 0", size)
	}
}

// TestInitializeQueueIntegration_RegistersQueueJob verifies that after
// InitializeQueueIntegration runs, the queue.HydrateJob path can rebuild
// an EventListenerJob from raw payload bytes. This exercises the
// queue.RegisterJob call inside InitializeQueueIntegration.
func TestInitializeQueueIntegration_RegistersQueueJob(t *testing.T) {
	InitializeQueueIntegration(nil, nil, nil)

	job := &EventListenerJob{
		EventType:    "init.event",
		ListenerType: "events.h22Listener",
	}
	payload, err := queue.MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	hydrated, err := queue.HydrateJob(payload)
	if err != nil {
		t.Fatalf("HydrateJob: %v", err)
	}
	hj, ok := hydrated.(*EventListenerJob)
	if !ok {
		t.Fatalf("HydrateJob returned %T, want *EventListenerJob", hydrated)
	}
	if hj.ListenerType != "events.h22Listener" || hj.EventType != "init.event" {
		t.Fatalf("hydrated job mismatch: %+v", hj)
	}
}

// TestRegisterListenerFactory_Concurrent locks in the H-23-style guarantee
// for the package-level listener registry added by H-22. Concurrent
// RegisterListenerFactory + lookupListenerFactory must be race-clean under
// -race.
func TestRegisterListenerFactory_Concurrent(t *testing.T) {
	defer UnregisterListenerFactory("events.h22Listener")

	const iters = 500
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			RegisterListenerFactory("events.h22Listener", func() Listener { return &h22Listener{} })
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			_, _ = lookupListenerFactory("events.h22Listener")
		}
	}()
	wg.Wait()
}

// TestEventListenerJob_HandleCtx_HydratesFromRegistry asserts the H-22 core
// fix: a job whose listener was set to nil (the cross-process worker path,
// where the job came off the wire via JSON) hydrates the listener via the
// package registry on HandleCtx, instead of returning "listener not set".
func TestEventListenerJob_HandleCtx_HydratesFromRegistry(t *testing.T) {
	defer UnregisterListenerFactory("events.h22Listener")

	counter := &atomic.Int32{}
	RegisterListenerFactory("events.h22Listener", func() Listener {
		return &h22Listener{counter: counter}
	})

	// Pretend the job just came back from the wire: listener is nil.
	jobBytes, err := json.Marshal(EventListenerJob{
		Event:        "wire.event",
		EventType:    "wire.event",
		ListenerType: "events.h22Listener",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	job, err := EventJobFactory(jobBytes)
	if err != nil {
		t.Fatalf("EventJobFactory: %v", err)
	}
	hc, ok := job.(queue.HandleCtxer)
	if !ok {
		t.Fatal("job does not implement HandleCtxer")
	}
	if err := hc.HandleCtx(context.Background()); err != nil {
		t.Fatalf("HandleCtx: %v", err)
	}
	if got := counter.Load(); got != 1 {
		t.Fatalf("listener invocation count: got %d want 1", got)
	}
}
