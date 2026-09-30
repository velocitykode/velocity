package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// namedOnce is an event whose Name answers once and panics on every later
// call, so a failure report that resolves the name again is caught.
type namedOnce struct{ calls atomic.Int32 }

func (e *namedOnce) Name() string {
	if e.calls.Add(1) > 1 {
		panic("Name called again")
	}
	return "named.once"
}

// A listener's failure on a detached delivery is reported under the name
// its listeners were resolved by: the AsyncFailed carries the listener's
// own error and that name, and the recorder gets the listener's error,
// even when the event's Name panics on any later call.
func TestDetachedFailure_ReportsTheResolvedNameAndTheListenersError(t *testing.T) {
	d := NewDispatcher()
	collector := &failureCollector{}
	d.Listen(&AsyncFailed{}, collector)
	d.Listen("named.once", failingListener{})
	rec := &detachedRecords{}
	d.SetDetachedFailureRecorder(rec.record)

	event := &namedOnce{}
	if err := d.DispatchAsync(context.Background(), event); err != nil {
		t.Fatalf("DispatchAsync: %v", err)
	}
	// The recorder runs last in the delivery, after the AsyncFailed.
	waitFor(func() bool { return rec.count() > 0 })
	if got := rec.count(); got != 1 {
		t.Fatalf("recorder calls = %d, want 1", got)
	}
	rec.mu.Lock()
	recorded := rec.errs[0]
	rec.mu.Unlock()
	if !errors.Is(recorded, errListenerBroke) {
		t.Errorf("recorded failure = %v, want the listener's error", recorded)
	}
	failures := collector.snapshot()
	if len(failures) != 1 {
		t.Fatalf("AsyncFailed delivered %d times, want 1", len(failures))
	}
	if !errors.Is(failures[0].Err, errListenerBroke) {
		t.Errorf("AsyncFailed.Err = %v, want the listener's error", failures[0].Err)
	}
	if failures[0].EventName != "named.once" {
		t.Errorf("AsyncFailed.EventName = %q, want %q", failures[0].EventName, "named.once")
	}
	if got := event.calls.Load(); got != 1 {
		t.Errorf("Name calls = %d, want 1", got)
	}
}
