package events

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// panicingListener panics on Handle — used to verify recovery in both
// AsyncDispatcher.Push and DefaultDispatcher.DispatchAsync fallback.
type panicingListener struct {
	handled atomic.Int32
}

func (p *panicingListener) Handle(event interface{}) error {
	p.handled.Add(1)
	panic("listener boom")
}
func (p *panicingListener) ShouldQueue() bool { return false }

// TestAsyncDispatcher_Push_RecoversPanic verifies that a listener panic in
// the async goroutine does not tear down the process.
func TestAsyncDispatcher_Push_RecoversPanic(t *testing.T) {
	ad := NewAsyncDispatcher()
	listener := &panicingListener{}

	// Immediate dispatch — no delay — goroutine path.
	if err := ad.Push("event", listener, 0); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	// Wait for the goroutine to schedule and panic.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if listener.handled.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if listener.handled.Load() == 0 {
		t.Fatal("expected listener to have been called")
	}

	// Delayed dispatch — time.AfterFunc path.
	listener2 := &panicingListener{}
	if err := ad.Push("event", listener2, 10*time.Millisecond); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	deadline = time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if listener2.handled.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if listener2.handled.Load() == 0 {
		t.Fatal("expected delayed listener to have been called")
	}
}

// TestDispatcher_DispatchAsync_Fallback_RecoversPanic verifies that when
// no queue is configured, the fallback goroutine in DispatchAsync
// recovers from listener panics.
func TestDispatcher_DispatchAsync_Fallback_RecoversPanic(t *testing.T) {
	d := NewDispatcher()
	// Ensure queue is nil — we want the fallback goroutine path.
	if d.queue != nil {
		t.Fatal("queue must be nil for fallback path")
	}

	var ran atomic.Int32
	d.Listen("fallback.boom", listenerFunc(func(event interface{}) error {
		ran.Add(1)
		panic("fallback listener boom")
	}))

	if err := d.DispatchAsync("fallback.boom"); err != nil {
		t.Fatalf("DispatchAsync failed: %v", err)
	}

	// Wait for the goroutine to run.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ran.Load() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ran.Load() == 0 {
		t.Fatal("expected listener to have been invoked")
	}

	// Dispatcher must remain usable.
	var followup atomic.Int32
	d.Listen("followup", listenerFunc(func(event interface{}) error {
		followup.Add(1)
		return nil
	}))
	if err := d.Dispatch("followup"); err != nil {
		t.Fatalf("Dispatch failed after panic: %v", err)
	}
	if followup.Load() != 1 {
		t.Fatalf("expected follow-up listener to run, got %d", followup.Load())
	}
}

// listenerFunc adapts a func to the Listener interface.
type listenerFunc func(event interface{}) error

func (f listenerFunc) Handle(event interface{}) error { return f(event) }
func (f listenerFunc) ShouldQueue() bool              { return false }

// Guard against accidentally shadowing stdlib sync.
var _ sync.Locker = (*sync.Mutex)(nil)
