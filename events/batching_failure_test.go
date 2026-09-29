package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A panic while a flush dispatches an entry (here the event's Name) is
// returned as an error on the same path as a listener failure: the entry
// stays queued, the flush state is cleared, and the next Flush delivers
// it.
func TestBatchingFlush_PanicIsAnErrorAndTheNextFlushDelivers(t *testing.T) {
	d := NewBatchingDispatcher(10, time.Hour)
	var seen atomic.Int32
	d.Listen("evt", tallyListener{n: &seen})
	name := hostile.New(t, hostile.Panic, nil)
	if err := d.Dispatch(context.Background(), panickingNameEvent{c: name}); err != nil {
		t.Fatalf("Dispatch = %v", err)
	}

	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("panic escaped Flush: %v", p)
			}
		}()
		err = d.Flush()
	}()
	var rp contract.RecoveredPanic
	if !errors.As(err, &rp) {
		t.Fatalf("Flush = %v, want the recovered panic", err)
	}
	if got := d.GetBatchSize(); got != 1 {
		t.Fatalf("batch size after the failed flush = %d, want 1", got)
	}
	name.Disarm()
	if err := d.Flush(); err != nil {
		t.Fatalf("second Flush = %v", err)
	}
	if got := seen.Load(); got != 1 {
		t.Errorf("listener handled %d events, want 1", got)
	}
}

// The background flush has no caller to return a failure to, so its
// deliveries are detached: a failing listener's failure is dispatched as
// an AsyncFailed and recorded once, and the entry is not requeued.
func TestBatchingBackgroundFlush_FailuresAreDeliveredDetached(t *testing.T) {
	d := NewBatchingDispatcher(10, 5*time.Millisecond)
	var handled, recorded, asyncFailed atomic.Int32
	d.Listen("evt", listenerFunc(func(context.Context, interface{}) error {
		handled.Add(1)
		return errListenerBroke
	}))
	d.Listen("events.listener.failed", tallyListener{n: &asyncFailed})
	d.SetDetachedFailureRecorder(func(context.Context, error, any) { recorded.Add(1) })
	d.Start()
	t.Cleanup(d.Stop)

	if err := d.Dispatch(context.Background(), "evt"); err != nil {
		t.Fatalf("Dispatch = %v", err)
	}
	waitFor(func() bool { return recorded.Load() > 0 })
	// Several more ticks: a requeued entry would be delivered again.
	time.Sleep(50 * time.Millisecond)

	if got := handled.Load(); got != 1 {
		t.Errorf("listener handled the event %d times, want 1", got)
	}
	if got := recorded.Load(); got != 1 {
		t.Errorf("recorded %d failed deliveries, want 1", got)
	}
	if got := asyncFailed.Load(); got != 1 {
		t.Errorf("AsyncFailed dispatched %d times, want 1", got)
	}
	if got := d.GetBatchSize(); got != 0 {
		t.Errorf("batch size = %d, want 0", got)
	}
}

// A listener running on the background flush goroutine may stop the
// dispatcher: Stop returns instead of waiting for the goroutine it runs on,
// and the loop still ends.
func TestBatchingStop_FromTheFlushGoroutineReturns(t *testing.T) {
	d := NewBatchingDispatcher(10, 5*time.Millisecond)
	stopped := make(chan struct{})
	d.Listen("evt", listenerFunc(func(context.Context, interface{}) error {
		d.Stop()
		close(stopped)
		return nil
	}))
	d.Start()
	if err := d.Dispatch(context.Background(), "evt"); err != nil {
		t.Fatalf("Dispatch = %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop called from the flush goroutine deadlocked")
	}
	within(t, "Stop after the loop stopped itself", d.Stop)
}

// A debounced delivery whose listener dispatches the same event again
// leaves the new pending delivery in place, so Stop still cancels it.
func TestDebouncing_RedispatchFromAListenerStaysPending(t *testing.T) {
	d := NewDebouncingDispatcher(50 * time.Millisecond)
	var handled atomic.Int32
	first := make(chan struct{})
	d.Listen("evt", listenerFunc(func(ctx context.Context, event interface{}) error {
		if handled.Add(1) == 1 {
			_ = d.Dispatch(ctx, event)
			close(first)
		}
		return nil
	}))
	if err := d.Dispatch(context.Background(), "evt"); err != nil {
		t.Fatalf("Dispatch = %v", err)
	}
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("the first debounced delivery never ran")
	}
	// Let the first delivery's timer callback finish.
	time.Sleep(10 * time.Millisecond)
	if got := d.GetPendingCount(); got != 1 {
		t.Errorf("pending = %d, want 1 (the listener's dispatch)", got)
	}
	d.Stop()
	time.Sleep(100 * time.Millisecond)
	if got := handled.Load(); got != 1 {
		t.Errorf("listener handled %d events after Stop, want 1", got)
	}
}
