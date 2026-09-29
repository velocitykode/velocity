package router

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A listener that stops the async pool it runs on cannot wait for that
// pool: ShutdownEventDispatcher returns an error at once, the pool stops
// taking events, and a stop from outside then waits for the drain.
func TestShutdownEventDispatcher_FromItsOwnListenerDoesNotWait(t *testing.T) {
	r := NewV2()
	var stopErr atomic.Pointer[error]
	returned := make(chan struct{})
	r.SetAsyncEventDispatcher(func(ctx context.Context, _ interface{}) error {
		err := r.ShutdownEventDispatcher(context.Background())
		stopErr.Store(&err)
		close(returned)
		return nil
	}, 1, 4)
	if err := r.events.Dispatcher()(context.Background(), "stop"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hostile.Within(t, hostile.Deadline, func() { <-returned })
	if p := stopErr.Load(); p == nil || *p == nil {
		t.Fatal("ShutdownEventDispatcher from its own listener returned nil; want an error saying it cannot wait")
	} else if !errors.Is(*p, errEventDispatcherStopped) {
		t.Errorf("error = %v, want one wrapping errEventDispatcherStopped", *p)
	}
	if err := r.events.Dispatcher()(context.Background(), "late"); !errors.Is(err, errEventDispatcherStopped) {
		t.Errorf("dispatch after the stop = %v, want errEventDispatcherStopped", err)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
			t.Errorf("ShutdownEventDispatcher from outside = %v, want nil once drained", err)
		}
	})
}

// A listener that installs a new async dispatcher stops its own pool
// without waiting for it: the call returns, and the new pool delivers.
func TestSetAsyncEventDispatcher_FromItsOwnListenerDoesNotWait(t *testing.T) {
	r := NewV2()
	shutdownOnCleanup(t, r)
	next := newRecordingTarget(t, false)
	returned := make(chan struct{})
	var once atomic.Bool
	r.SetAsyncEventDispatcher(func(ctx context.Context, _ interface{}) error {
		if once.CompareAndSwap(false, true) {
			r.SetAsyncEventDispatcher(next.dispatch, 1, 4)
			close(returned)
		}
		return nil
	}, 1, 4)
	if err := r.events.Dispatcher()(context.Background(), "swap"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hostile.Within(t, hostile.Deadline, func() { <-returned })
	if err := r.events.Dispatcher()(context.Background(), "next"); err != nil {
		t.Fatalf("dispatch to the new pool: %v", err)
	}
	waitForCount(t, next, 1, "new pool's target")
}

// A stop that overlaps one already draining waits for that drain or its
// own ctx, whichever comes first: it neither waits past its deadline for
// a stop with a longer one, nor returns before the drain it joined ends.
func TestShutdownEventDispatcher_OverlappingStopWaitsForItsOwnContext(t *testing.T) {
	r := NewV2()
	target := newRecordingTarget(t, true)
	r.SetAsyncEventDispatcher(target.dispatch, 1, 4)
	if err := r.events.Dispatcher()(context.Background(), "held"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	select {
	case <-target.entered:
	case <-time.After(hostile.Deadline):
		t.Fatal("pool never started delivering")
	}

	firstDone := make(chan error, 1)
	go func() { //safe-goroutine: the test releases the target below and waits for it
		firstDone <- r.ShutdownEventDispatcher(context.Background())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	hostile.Within(t, hostile.Deadline, func() {
		if err := r.ShutdownEventDispatcher(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("overlapping ShutdownEventDispatcher = %v, want its own deadline", err)
		}
	})

	target.release()
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-firstDone; err != nil {
			t.Errorf("owning ShutdownEventDispatcher = %v, want nil after the drain", err)
		}
		if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
			t.Errorf("ShutdownEventDispatcher after the drain = %v, want nil", err)
		}
	})
}
