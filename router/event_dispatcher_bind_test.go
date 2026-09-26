package router

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recordingTarget records every event delivered to it; with a gate, each
// delivery waits on it first.
type recordingTarget struct {
	mu        sync.Mutex
	events    []interface{}
	gate      chan struct{}
	entered   chan struct{}
	once      sync.Once
	closeGate sync.Once
}

// newRecordingTarget returns a target. A gated one registers its release
// with t.Cleanup, so a failing test never leaves a worker or a dispatching
// goroutine blocked on it.
func newRecordingTarget(tb testing.TB, gated bool) *recordingTarget {
	rt := &recordingTarget{entered: make(chan struct{})}
	if gated {
		rt.gate = make(chan struct{})
		tb.Cleanup(rt.release)
	}
	return rt
}

// release opens the gate; safe to call more than once.
func (t *recordingTarget) release() {
	if t.gate != nil {
		t.closeGate.Do(func() { close(t.gate) })
	}
}

func (t *recordingTarget) dispatch(_ context.Context, event interface{}) error {
	t.once.Do(func() { close(t.entered) })
	if t.gate != nil {
		<-t.gate
	}
	t.mu.Lock()
	t.events = append(t.events, event)
	t.mu.Unlock()
	return nil
}

func (t *recordingTarget) got() []interface{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]interface{}(nil), t.events...)
}

// shutdownOnCleanup drains the router's current pool when the test ends.
// Cleanups run last-registered first, so a gated target created after
// this call is released before the drain waits on it.
func shutdownOnCleanup(tb testing.TB, r *VelocityRouterV2) {
	tb.Cleanup(func() { _ = r.ShutdownEventDispatcher(context.Background()) })
}

func waitForCount(t *testing.T, target *recordingTarget, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(target.got()) >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s: got %d events, want %d", what, len(target.got()), want)
}

// BindEventDispatcher keeps async delivery: the pool keeps running and the
// events it dequeues afterwards reach the bound function.
func TestBindEventDispatcher_KeepsAsyncDelivery(t *testing.T) {
	r := NewV2()
	first := newRecordingTarget(t, false)
	r.SetAsyncEventDispatcher(first.dispatch, 1, 8)
	shutdownOnCleanup(t, r)
	bound := newRecordingTarget(t, true)

	r.BindEventDispatcher(bound.dispatch)

	// A gated target would block a synchronous caller; the async enqueuer
	// returns at once. The dispatching goroutine is joined on every exit:
	// the deferred release unblocks it if delivery regressed to sync.
	done := make(chan struct{})
	var dispatchErr error
	go func() {
		defer close(done)
		dispatchErr = r.eventDispatcher(context.Background(), "after-bind")
	}()
	defer func() {
		bound.release()
		<-done
	}()
	select {
	case <-done:
		if dispatchErr != nil {
			t.Fatalf("dispatch after bind: %v", dispatchErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch after BindEventDispatcher blocked: delivery is no longer async")
	}
	bound.release()
	waitForCount(t, bound, 1, "bound target")
	if got := first.got(); len(got) != 0 {
		t.Errorf("original target received %v after the bind", got)
	}
}

// BindEventDispatcher in sync mode behaves as SetEventDispatcher.
func TestBindEventDispatcher_SyncModeAssigns(t *testing.T) {
	r := NewV2()
	target := newRecordingTarget(t, false)
	r.BindEventDispatcher(target.dispatch)
	if err := r.eventDispatcher(context.Background(), "sync"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := target.got(); len(got) != 1 {
		t.Fatalf("sync bind delivered %d events, want 1", len(got))
	}
}

// A pool retired by a timed-out shutdown keeps delivering its buffered
// events to its own target: neither a replacement pool nor a bind on the
// replacement redirects it.
func TestBindEventDispatcher_RetiredPoolKeepsItsTarget(t *testing.T) {
	r := NewV2()
	newTarget := newRecordingTarget(t, false)
	boundTarget := newRecordingTarget(t, false)

	// Registered before the gated target, so on every exit the gate opens
	// first and the old pool's worker can then finish its drain.
	shutdownOnCleanup(t, r)
	oldTarget := newRecordingTarget(t, true)
	r.SetAsyncEventDispatcher(oldTarget.dispatch, 1, 8)
	for _, ev := range []string{"old-1", "old-2", "old-3"} {
		if err := r.eventDispatcher(context.Background(), ev); err != nil {
			t.Fatalf("dispatch %s: %v", ev, err)
		}
	}
	// The single worker holds old-1 inside the gated target; old-2 and
	// old-3 wait in the buffer.
	select {
	case <-oldTarget.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old pool never started delivering")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := r.ShutdownEventDispatcher(ctx); err == nil {
		t.Fatal("ShutdownEventDispatcher returned nil although the old pool could not drain")
	}

	// Replacement pool, then a bind on it, while the old pool still drains.
	r.SetAsyncEventDispatcher(newTarget.dispatch, 1, 8)
	shutdownOnCleanup(t, r)
	r.BindEventDispatcher(boundTarget.dispatch)

	oldTarget.release()
	waitForCount(t, oldTarget, 3, "retired pool's target")

	if got := newTarget.got(); len(got) != 0 {
		t.Errorf("replacement pool's original target received %v", got)
	}
	if got := boundTarget.got(); len(got) != 0 {
		t.Errorf("target bound on the replacement pool received the retired pool's events %v", got)
	}

	if err := r.eventDispatcher(context.Background(), "new-1"); err != nil {
		t.Fatalf("dispatch new-1: %v", err)
	}
	waitForCount(t, boundTarget, 1, "bound target on the replacement pool")
}
