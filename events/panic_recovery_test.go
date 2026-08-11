package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// countingPanicListener panics on Handle — used to verify recovery in both
// AsyncDispatcher.Push and DefaultDispatcher.DispatchAsync fallback.
type countingPanicListener struct {
	handled atomic.Int32
}

func (p *countingPanicListener) Handle(ctx context.Context, event interface{}) error {
	p.handled.Add(1)
	panic("listener boom")
}
func (p *countingPanicListener) Async() bool { return false }

// TestAsyncDispatcher_Push_RecoversPanic verifies that a listener panic in
// the async goroutine does not tear down the process.
func TestAsyncDispatcher_Push_RecoversPanic(t *testing.T) {
	ad := NewAsyncDispatcher()
	listener := &countingPanicListener{}

	// Immediate dispatch — no delay — goroutine path.
	if err := ad.Push(context.Background(), "event", listener, 0); err != nil {
		t.Fatalf("push failed: %v", err)
	}

	testsync.Eventually(t, func() bool { return listener.handled.Load() > 0 }, time.Second, "immediate listener invoked")

	// Delayed dispatch — time.AfterFunc path.
	listener2 := &countingPanicListener{}
	if err := ad.Push(context.Background(), "event", listener2, 10*time.Millisecond); err != nil {
		t.Fatalf("push failed: %v", err)
	}
	testsync.Eventually(t, func() bool { return listener2.handled.Load() > 0 }, time.Second, "delayed listener invoked")
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
	d.Listen("fallback.boom", listenerFunc(func(ctx context.Context, event interface{}) error {
		ran.Add(1)
		panic("fallback listener boom")
	}))

	if err := d.DispatchAsync(context.Background(), "fallback.boom"); err != nil {
		t.Fatalf("DispatchAsync failed: %v", err)
	}

	testsync.Eventually(t, func() bool { return ran.Load() > 0 }, time.Second, "fallback listener invoked")

	// Dispatcher must remain usable.
	var followup atomic.Int32
	d.Listen("followup", listenerFunc(func(ctx context.Context, event interface{}) error {
		followup.Add(1)
		return nil
	}))
	if err := d.Dispatch(context.Background(), "followup"); err != nil {
		t.Fatalf("Dispatch failed after panic: %v", err)
	}
	if followup.Load() != 1 {
		t.Fatalf("expected follow-up listener to run, got %d", followup.Load())
	}
}

// listenerFunc adapts a func to the Listener interface.
type listenerFunc func(ctx context.Context, event interface{}) error

func (f listenerFunc) Handle(ctx context.Context, event interface{}) error { return f(ctx, event) }
func (f listenerFunc) Async() bool                                         { return false }

// Guard against accidentally shadowing stdlib sync.
var _ sync.Locker = (*sync.Mutex)(nil)
