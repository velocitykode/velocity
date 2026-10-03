package router

import (
	"context"
	"errors"
	"github.com/velocitykode/velocity/contract"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A listener that stops the router whose pool it runs on cannot wait for
// that pool: Shutdown is refused at once and changes nothing, the pool
// keeps taking events, and a stop from outside then waits for the drain.
func TestShutdown_FromItsOwnListenerIsRefused(t *testing.T) {
	r := NewV2()
	var stopErr atomic.Pointer[error]
	returned := make(chan struct{})
	var once atomic.Bool
	r.SetAsyncEventDispatcher(func(ctx context.Context, _ interface{}) error {
		if once.CompareAndSwap(false, true) {
			err := r.Shutdown(context.Background())
			stopErr.Store(&err)
			close(returned)
		}
		return nil
	}, 1, 4)
	if err := r.events.Dispatcher()(context.Background(), "stop"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hostile.Within(t, hostile.Deadline, func() { <-returned })
	if p := stopErr.Load(); p == nil || !errors.Is(*p, contract.ErrStopFromOwnWork) {
		t.Fatalf("Shutdown from its own listener = %v, want contract.ErrStopFromOwnWork", stopErr.Load())
	}
	if r.requests.Stopping() {
		t.Fatal("the refused Shutdown closed the router's admission")
	}
	if err := r.events.Dispatcher()(context.Background(), "after"); err != nil {
		t.Errorf("dispatch after the refused stop = %v, want the pool still taking events", err)
	}
	send := r.events.Dispatcher()
	hostile.Within(t, hostile.Deadline, func() {
		if err := r.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown from outside = %v, want nil once drained", err)
		}
	})
	if err := send(context.Background(), "late"); !errors.Is(err, errEventDispatcherStopped) {
		t.Errorf("dispatch on the stopped pool = %v, want errEventDispatcherStopped", err)
	}
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
func TestRouterShutdown_OverlappingStopWaitsForItsOwnContext(t *testing.T) {
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
		firstDone <- r.Shutdown(context.Background())
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	hostile.Within(t, hostile.Deadline, func() {
		if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("overlapping Router.Shutdown = %v, want its own deadline", err)
		}
	})

	target.release()
	hostile.Within(t, hostile.Deadline, func() {
		if err := <-firstDone; err != nil {
			t.Errorf("owning Router.Shutdown = %v, want nil after the drain", err)
		}
		if err := r.Shutdown(context.Background()); err != nil {
			t.Errorf("Router.Shutdown after the drain = %v, want nil", err)
		}
	})
}
