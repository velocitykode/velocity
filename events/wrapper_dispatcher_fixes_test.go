package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// reentrantRLListener re-enters the same RateLimitedDispatcher from inside
// Handle exactly once. Before the B36 fix RateLimitedDispatcher.Dispatch held
// countMu via defer across the inner fan-out, so this re-entrant Dispatch
// blocked forever trying to re-acquire countMu.
type reentrantRLListener struct {
	d    *RateLimitedDispatcher
	done int32
}

func (l *reentrantRLListener) Handle(ctx context.Context, event interface{}) error {
	if atomic.CompareAndSwapInt32(&l.done, 0, 1) {
		return l.d.Dispatch(ctx, &simpleEvent{name: "inner.event"})
	}
	return nil
}

func (l *reentrantRLListener) ShouldQueue() bool { return false }

// TestRateLimitedDispatcher_ReentrantDispatchNoDeadlock proves B36: a listener
// that re-dispatches through the same RateLimitedDispatcher must complete
// rather than deadlock on countMu.
func TestRateLimitedDispatcher_ReentrantDispatchNoDeadlock(t *testing.T) {
	dispatcher := NewRateLimitedDispatcher(10, time.Hour)

	var inner int32
	dispatcher.Listen("inner.event", &countingListener{counter: &inner})
	dispatcher.Listen("outer.event", &reentrantRLListener{d: dispatcher})

	done := make(chan error, 1)
	go func() {
		done <- dispatcher.Dispatch(context.Background(), &simpleEvent{name: "outer.event"})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("re-entrant dispatch returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-entrant dispatch deadlocked (countMu held across fan-out)")
	}

	if atomic.LoadInt32(&inner) != 1 {
		t.Errorf("inner listener fired %d times, want 1", atomic.LoadInt32(&inner))
	}
}

// flakyListener fails the first time it sees the designated failure event,
// then succeeds on every subsequent call. successes counts non-error handles.
type flakyListener struct {
	mu        sync.Mutex
	failName  string
	failed    bool
	successes int32
}

func (l *flakyListener) Handle(ctx context.Context, event interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ev, ok := event.(*simpleEvent); ok && ev.name == l.failName && !l.failed {
		l.failed = true
		return errors.New("flaky boom")
	}
	atomic.AddInt32(&l.successes, 1)
	return nil
}

func (l *flakyListener) ShouldQueue() bool { return false }

// TestBatchingDispatcher_FlushPartialFailureResumes proves B37: when a flush
// fails partway, the failing entry plus the remainder stay buffered so a retry
// Flush resumes and no entries are silently dropped.
func TestBatchingDispatcher_FlushPartialFailureResumes(t *testing.T) {
	// Large batch + no Start(): events accumulate without the interval flush
	// racing the manual Flush calls below.
	dispatcher := NewBatchingDispatcher(100, time.Hour)

	lst := &flakyListener{failName: "e2"}
	dispatcher.Listen("e1", lst)
	dispatcher.Listen("e2", lst)
	dispatcher.Listen("e3", lst)

	dispatcher.Dispatch(context.Background(), &simpleEvent{name: "e1"})
	dispatcher.Dispatch(context.Background(), &simpleEvent{name: "e2"})
	dispatcher.Dispatch(context.Background(), &simpleEvent{name: "e3"})

	// First flush: e1 delivers, e2 fails -> e2 and e3 remain buffered.
	if err := dispatcher.Flush(); err == nil {
		t.Fatal("expected error from first flush")
	}
	if got := dispatcher.GetBatchSize(); got != 2 {
		t.Fatalf("after failed flush batch size = %d, want 2 (failed entry + remainder)", got)
	}

	// Retry: e2 now succeeds, e3 delivers. Buffer drains.
	if err := dispatcher.Flush(); err != nil {
		t.Fatalf("retry flush failed: %v", err)
	}
	if got := dispatcher.GetBatchSize(); got != 0 {
		t.Fatalf("after retry batch size = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&lst.successes); got != 3 {
		t.Errorf("delivered %d events, want 3 (no entry dropped)", got)
	}
}

// TestBatchingDispatcher_DoubleStop proves B37: Stop is idempotent and does not
// panic on a double close of stopCh.
func TestBatchingDispatcher_DoubleStop(t *testing.T) {
	dispatcher := NewBatchingDispatcher(10, time.Hour)
	dispatcher.Start()
	dispatcher.Stop()
	dispatcher.Stop() // must not panic
}

// TestTransactionalDispatcher_CommitPartialFailureResumes proves the async.go
// fix: a Commit that fails partway re-buffers the failing event plus the
// remainder so a retry Commit replays them.
func TestTransactionalDispatcher_CommitPartialFailureResumes(t *testing.T) {
	base := NewDispatcher()
	lst := &flakyListener{failName: "tx2"}
	base.Listen("tx1", lst)
	base.Listen("tx2", lst)
	base.Listen("tx3", lst)

	td := NewTransactionalDispatcher(base)
	td.BeginTransaction()
	td.DispatchAfterCommit(context.Background(), &simpleEvent{name: "tx1"})
	td.DispatchAfterCommit(context.Background(), &simpleEvent{name: "tx2"})
	td.DispatchAfterCommit(context.Background(), &simpleEvent{name: "tx3"})

	if err := td.Commit(context.Background()); err == nil {
		t.Fatal("expected error from first commit")
	}
	if got := td.pending.events; len(got) != 2 {
		t.Fatalf("after failed commit pending = %d, want 2 (failed + remainder)", len(got))
	}

	if err := td.Commit(context.Background()); err != nil {
		t.Fatalf("retry commit failed: %v", err)
	}
	if got := atomic.LoadInt32(&lst.successes); got != 3 {
		t.Errorf("delivered %d events, want 3 (no event lost)", got)
	}
}

// errListener always returns the same error. Used to prove FakeDispatcher.Until
// propagates dispatch errors.
type errListener struct{ err error }

func (l *errListener) Handle(ctx context.Context, event interface{}) error { return l.err }
func (l *errListener) ShouldQueue() bool                                   { return false }

// TestFakeDispatcher_UntilPropagatesError proves the fake.go fix: Until must
// return the error from a listener rather than discarding it.
func TestFakeDispatcher_UntilPropagatesError(t *testing.T) {
	f := NewFakeDispatcher()
	f.StopFaking()

	boom := errors.New("until boom")
	f.Listen("u.event", &errListener{err: boom})

	res, err := f.Until(context.Background(), &simpleEvent{name: "u.event"})
	if !errors.Is(err, boom) {
		t.Fatalf("Until err = %v, want %v", err, boom)
	}
	if res != nil {
		t.Errorf("Until result = %v, want nil", res)
	}

	// Success path still returns (nil, nil).
	res, err = f.Until(context.Background(), &simpleEvent{name: "u.noisten"})
	if err != nil || res != nil {
		t.Errorf("Until on no-listener event = (%v, %v), want (nil, nil)", res, err)
	}
}
