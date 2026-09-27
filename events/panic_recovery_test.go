package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

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
