package events

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBuffer_StandaloneDispatchDiscardsOnFlush ensures a standalone buffer
// (no underlying dispatcher) discards events on Flush without erroring.
func TestBuffer_StandaloneDispatchDiscardsOnFlush(t *testing.T) {
	ctx := context.Background()
	buf := Buffer(ctx)
	if err := buf.Dispatch(context.Background(), "event1"); err != nil {
		t.Fatalf("Dispatch returned %v", err)
	}
	if got := buf.Pending(); got != 1 {
		t.Fatalf("Pending = %d, want 1", got)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush returned %v", err)
	}
	if got := buf.Pending(); got != 0 {
		t.Fatalf("Pending after Flush = %d, want 0", got)
	}
}

// TestBuffer_WithBufferAttachesAndFlushesInOrder verifies WithBuffer
// installs a buffer reachable via Buffer(ctx) and that Flush forwards
// events in dispatch order.
func TestBuffer_WithBufferAttachesAndFlushesInOrder(t *testing.T) {
	var got []interface{}
	flushFn := func(e BufferedEvent) error {
		got = append(got, e.Event())
		return nil
	}
	ctx, buf := WithBuffer(context.Background(), flushFn)

	if !HasBuffer(ctx) {
		t.Fatal("HasBuffer returned false after WithBuffer")
	}
	if Buffer(ctx) != buf {
		t.Fatal("Buffer(ctx) did not return the buffer attached by WithBuffer")
	}

	for _, e := range []string{"a", "b", "c"} {
		if err := Buffer(ctx).Dispatch(context.Background(), e); err != nil {
			t.Fatalf("Dispatch %q: %v", e, err)
		}
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("flushed events = %v, want [a b c]", got)
	}
}

// TestBuffer_FlushStopsOnFirstError verifies the flush stops at the first
// failing event and reports that error. The remaining un-flushed events
// stay in the buffer so a retry can resume.
func TestBuffer_FlushStopsOnFirstError(t *testing.T) {
	bang := errors.New("boom")
	calls := 0
	flushFn := func(e BufferedEvent) error {
		calls++
		if e.Event() == "fail" {
			return bang
		}
		return nil
	}
	_, buf := WithBuffer(context.Background(), flushFn)
	for _, e := range []string{"ok1", "fail", "ok2"} {
		_ = buf.Dispatch(context.Background(), e)
	}
	err := buf.Flush()
	if !errors.Is(err, bang) {
		t.Fatalf("Flush err = %v, want %v", err, bang)
	}
	if calls != 2 {
		t.Fatalf("flush called %d times, want 2 (stop after first failure)", calls)
	}
	// The failing entry plus remainder must still be pending so a retry
	// can resume from where it stopped.
	if got := buf.Pending(); got != 2 {
		t.Fatalf("Pending after partial-failure Flush = %d, want 2", got)
	}
}

// TestFlush_PartialFailure_Resumable verifies that on partial failure the
// buffer is left in a state where the caller can call Flush again and
// the previously-failed entry is retried (not the prefix that already
// succeeded). This is the contract that lets Transaction recover from a
// transient downstream error.
func TestFlush_PartialFailure_Resumable(t *testing.T) {
	var seen []string
	failNext := true
	flushFn := func(e BufferedEvent) error {
		ev := e.Event().(string)
		if ev == "fail" && failNext {
			failNext = false
			return errors.New("transient")
		}
		seen = append(seen, ev)
		return nil
	}
	_, buf := WithBuffer(context.Background(), flushFn)
	for _, e := range []string{"ok1", "fail", "ok2"} {
		_ = buf.Dispatch(context.Background(), e)
	}

	// First Flush: stops at "fail".
	if err := buf.Flush(); err == nil {
		t.Fatal("Flush #1: want error, got nil")
	}
	if len(seen) != 1 || seen[0] != "ok1" {
		t.Fatalf("after Flush #1 seen = %v, want [ok1]", seen)
	}
	if got := buf.Pending(); got != 2 {
		t.Fatalf("Pending after Flush #1 = %d, want 2", got)
	}

	// Second Flush: retry succeeds, ok1 is NOT redelivered.
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush #2: %v", err)
	}
	if len(seen) != 3 || seen[0] != "ok1" || seen[1] != "fail" || seen[2] != "ok2" {
		t.Fatalf("after Flush #2 seen = %v, want [ok1 fail ok2]", seen)
	}
	if got := buf.Pending(); got != 0 {
		t.Fatalf("Pending after retry Flush = %d, want 0", got)
	}
}

// TestBuffer_DropDiscards verifies Drop empties the buffer and prevents
// further recording.
func TestBuffer_DropDiscards(t *testing.T) {
	flushed := 0
	flushFn := func(e BufferedEvent) error { flushed++; return nil }
	_, buf := WithBuffer(context.Background(), flushFn)
	_ = buf.Dispatch(context.Background(), "a")
	_ = buf.Dispatch(context.Background(), "b")
	buf.Drop()
	if got := buf.Pending(); got != 0 {
		t.Fatalf("Pending after Drop = %d, want 0", got)
	}
	// Subsequent Dispatch must not record.
	_ = buf.Dispatch(context.Background(), "c")
	if got := buf.Pending(); got != 0 {
		t.Fatalf("Pending after post-drop Dispatch = %d, want 0", got)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush after Drop returned %v", err)
	}
	if flushed != 0 {
		t.Fatalf("flushed = %d, want 0 (Drop should suppress)", flushed)
	}
}

// TestBuffer_InstallBufferLookupViaCtx verifies InstallBuffer fills the
// holder slot prepared by PrepareBuffer so Buffer(ctx) can find it (the
// path orm.Manager.Transaction relies on).
func TestBuffer_InstallBufferLookupViaCtx(t *testing.T) {
	ctx := PrepareBuffer(context.Background())
	buf, release := InstallBuffer(ctx, func(e BufferedEvent) error { return nil })
	defer release()
	if Buffer(ctx) != buf {
		t.Fatalf("Buffer(ctx) did not return the installed buffer")
	}
	release()
	if HasBuffer(ctx) {
		t.Fatalf("HasBuffer returned true after release")
	}
}

// TestBuffer_PrepareBufferIdempotent verifies that PrepareBuffer is a
// no-op when ctx already carries a holder.
func TestBuffer_PrepareBufferIdempotent(t *testing.T) {
	ctx1 := PrepareBuffer(context.Background())
	ctx2 := PrepareBuffer(ctx1)
	if ctx1 != ctx2 {
		t.Fatal("PrepareBuffer returned a different ctx for an already-prepared ctx")
	}
}

// TestBuffer_InstallBufferNoHolderStandalone verifies InstallBuffer on a
// ctx without a prepared holder returns a standalone buffer (no-op
// release) so Transaction can proceed safely without PrepareBuffer.
func TestBuffer_InstallBufferNoHolderStandalone(t *testing.T) {
	ctx := context.Background()
	buf, release := InstallBuffer(ctx, func(e BufferedEvent) error { return nil })
	defer release()
	if buf == nil {
		t.Fatal("InstallBuffer returned nil")
	}
	if HasBuffer(ctx) {
		t.Fatal("HasBuffer returned true on un-prepared ctx after InstallBuffer")
	}
}

// TestBuffer_InstallBufferNilCtx verifies the nil-ctx defensive path.
func TestBuffer_InstallBufferNilCtx(t *testing.T) {
	//lint:ignore SA1012 deliberate nil-ctx safety check.
	buf, release := InstallBuffer(nil, nil)
	defer release()
	if buf == nil {
		t.Fatal("InstallBuffer(nil) returned nil")
	}
}

// TestBuffer_NestedInnerDropOuterFlush verifies nested semantics: the
// inner handle truncates only events emitted within the inner scope on
// Drop, while the outer Flush forwards events accumulated in the outer
// scope before the inner started.
func TestBuffer_NestedInnerDropOuterFlush(t *testing.T) {
	var flushed []interface{}
	outerCtx, outer := WithBuffer(context.Background(), func(e BufferedEvent) error {
		flushed = append(flushed, e.Event())
		return nil
	})
	_ = Buffer(outerCtx).Dispatch(context.Background(), "outer-1")

	// Open a nested handle on the same ctx.
	_, inner := WithBuffer(outerCtx, nil)
	_ = Buffer(outerCtx).Dispatch(context.Background(), "inner-1")
	_ = Buffer(outerCtx).Dispatch(context.Background(), "inner-2")
	if outer.Pending() != 3 {
		t.Fatalf("outer.Pending = %d, want 3", outer.Pending())
	}
	// Inner rolls back: only inner events drop.
	inner.Drop()
	if outer.Pending() != 1 {
		t.Fatalf("outer.Pending after inner Drop = %d, want 1", outer.Pending())
	}
	// Inner Flush is a no-op.
	if err := inner.Flush(); err != nil {
		t.Fatalf("inner.Flush returned %v", err)
	}
	if len(flushed) != 0 {
		t.Fatalf("flushed by inner = %v, want []", flushed)
	}
	// Outer commits.
	if err := outer.Flush(); err != nil {
		t.Fatalf("outer.Flush: %v", err)
	}
	if len(flushed) != 1 || flushed[0] != "outer-1" {
		t.Fatalf("flushed = %v, want [outer-1]", flushed)
	}
}

// TestBuffer_NestedInnerCommitOuterRollback verifies inner-commit /
// outer-rollback drops everything (consistency with txn semantics).
func TestBuffer_NestedInnerCommitOuterRollback(t *testing.T) {
	var flushed []interface{}
	outerCtx, outer := WithBuffer(context.Background(), func(e BufferedEvent) error {
		flushed = append(flushed, e.Event())
		return nil
	})
	_ = Buffer(outerCtx).Dispatch(context.Background(), "outer-1")
	_, inner := WithBuffer(outerCtx, nil)
	_ = Buffer(outerCtx).Dispatch(context.Background(), "inner-1")
	if err := inner.Flush(); err != nil { // no-op
		t.Fatalf("inner.Flush: %v", err)
	}
	outer.Drop() // outer rollback
	if outer.Pending() != 0 {
		t.Fatalf("outer.Pending after outer Drop = %d, want 0", outer.Pending())
	}
	if err := outer.Flush(); err != nil {
		t.Fatalf("outer.Flush after Drop: %v", err)
	}
	if len(flushed) != 0 {
		t.Fatalf("flushed = %v, want []", flushed)
	}
}

// TestBuffer_FlushIdempotent verifies a second Flush is a no-op after a
// successful first Flush.
func TestBuffer_FlushIdempotent(t *testing.T) {
	calls := 0
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		calls++
		return nil
	})
	_ = buf.Dispatch(context.Background(), "a")
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush #1: %v", err)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush #2: %v", err)
	}
	if calls != 1 {
		t.Fatalf("flush calls = %d, want 1", calls)
	}
}

// TestBuffer_DispatchAfterFlushNoOp ensures events recorded after a
// successful Flush are silently dropped (defense against re-entry by
// listeners triggered during Flush).
func TestBuffer_DispatchAfterFlushNoOp(t *testing.T) {
	calls := 0
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		calls++
		return nil
	})
	_ = buf.Dispatch(context.Background(), "first")
	_ = buf.Flush()
	_ = buf.Dispatch(context.Background(), "after-flush")
	if buf.Pending() != 0 {
		t.Fatalf("Pending after post-flush Dispatch = %d, want 0", buf.Pending())
	}
	if calls != 1 {
		t.Fatalf("flush calls = %d, want 1", calls)
	}
}

// TestBuffer_NilEventIgnored verifies nil event values are silently
// ignored (matches DefaultDispatcher's nil rejection but without
// surfacing an error from a buffered call).
func TestBuffer_NilEventIgnored(t *testing.T) {
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error { return nil })
	_ = buf.Dispatch(context.Background(), nil)
	if got := buf.Pending(); got != 0 {
		t.Fatalf("Pending = %d after nil dispatch, want 0", got)
	}
}

// TestBuffer_NilCtxStandalone exercises the nil-ctx defensive path.
func TestBuffer_NilCtxStandalone(t *testing.T) {
	//lint:ignore SA1012 deliberate nil-ctx safety check.
	buf := Buffer(nil)
	if buf == nil {
		t.Fatal("Buffer(nil) returned nil")
	}
	//lint:ignore SA1012 deliberate nil-ctx safety check.
	if HasBuffer(nil) {
		t.Fatal("HasBuffer(nil) returned true")
	}
	//lint:ignore SA1012 deliberate nil-ctx safety check.
	_, child := WithBuffer(nil, nil)
	if child == nil {
		t.Fatal("WithBuffer(nil) returned nil")
	}
}

// TestBuffer_DispatchVariantsAllRecord verifies Dispatch/DispatchNow/
// DispatchAsync/DispatchAfter/Until all funnel into the same buffer.
func TestBuffer_DispatchVariantsAllRecord(t *testing.T) {
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error { return nil })
	_ = buf.Dispatch(context.Background(), "a")
	_ = buf.DispatchNow(context.Background(), "b")
	_ = buf.DispatchAsync(context.Background(), "c")
	_ = buf.DispatchAfter(context.Background(), "d", 1*time.Second)
	if _, err := buf.Until(context.Background(), "e"); err != nil {
		t.Fatalf("Until: %v", err)
	}
	if got := buf.Pending(); got != 5 {
		t.Fatalf("Pending = %d, want 5", got)
	}
}

// TestDispatchAfter_DelayPreserved verifies that DispatchAfter records
// the requested delay on the buffered entry so FlushFunc can route it
// through Dispatcher.DispatchAfter on flush. Without this fix, the delay
// was silently dropped and "after delay" events fired immediately.
func TestDispatchAfter_DelayPreserved(t *testing.T) {
	var entries []BufferedEvent
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		entries = append(entries, e)
		return nil
	})
	_ = buf.DispatchAfter(context.Background(), "delayed", 5*time.Second)
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Kind() != KindDispatchAfter {
		t.Fatalf("entry.Kind = %v, want KindDispatchAfter", entries[0].Kind())
	}
	if got := entries[0].Delay(); got != 5*time.Second {
		t.Fatalf("entry.Delay = %v, want 5s", got)
	}
	if got := entries[0].Event(); got != "delayed" {
		t.Fatalf("entry.Event = %v, want delayed", got)
	}
}

// TestDispatchAsync_KindPreserved verifies that the dispatch kind is
// preserved across the buffer boundary so listeners' ShouldQueue / async
// semantics flow through to the underlying dispatcher on Flush. Without
// this fix all variants collapsed to KindDispatch and async listeners
// fired synchronously on the commit goroutine.
func TestDispatchAsync_KindPreserved(t *testing.T) {
	var kinds []DispatchKind
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		kinds = append(kinds, e.Kind())
		return nil
	})
	_ = buf.Dispatch(context.Background(), "a")
	_ = buf.DispatchNow(context.Background(), "b")
	_ = buf.DispatchAsync(context.Background(), "c")
	_ = buf.DispatchAfter(context.Background(), "d", time.Second)
	if _, err := buf.Until(context.Background(), "e"); err != nil {
		t.Fatalf("Until: %v", err)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	want := []DispatchKind{KindDispatch, KindDispatchNow, KindDispatchAsync, KindDispatchAfter, KindUntil}
	if len(kinds) != len(want) {
		t.Fatalf("kinds len = %d, want %d", len(kinds), len(want))
	}
	for i, k := range kinds {
		if k != want[i] {
			t.Fatalf("kinds[%d] = %v, want %v", i, k, want[i])
		}
	}
}

// TestBuffer_FlushReentrantNoOp verifies that a Flush invoked from inside
// a FlushFunc is silently a no-op so the outer Flush retains control of
// the drain (prevents infinite recursion if a listener tries to commit).
func TestBuffer_FlushReentrantNoOp(t *testing.T) {
	var inner *BufferedDispatcher
	calls := 0
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		calls++
		// Re-entrant Flush from inside the flush callback.
		if err := inner.Flush(); err != nil {
			t.Fatalf("re-entrant Flush returned %v", err)
		}
		return nil
	})
	inner = buf
	_ = buf.Dispatch(context.Background(), "a")
	_ = buf.Dispatch(context.Background(), "b")
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if calls != 2 {
		t.Fatalf("flushFn calls = %d, want 2 (re-entrant Flush must not double-fire)", calls)
	}
}

// TestBuffer_FlushNoSink verifies the no-FlushFunc path: a buffer wired
// with a nil sink discards entries on Flush and transitions to flushed.
func TestBuffer_FlushNoSink(t *testing.T) {
	_, buf := WithBuffer(context.Background(), nil)
	_ = buf.Dispatch(context.Background(), "dropped")
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush with nil sink: %v", err)
	}
	if buf.Pending() != 0 {
		t.Fatalf("Pending after nil-sink Flush = %d, want 0", buf.Pending())
	}
	// Subsequent Dispatch is a no-op (flushed).
	_ = buf.Dispatch(context.Background(), "after")
	if buf.Pending() != 0 {
		t.Fatalf("Pending after post-flush Dispatch = %d, want 0", buf.Pending())
	}
}

// TestDispatchKind_String exercises the diagnostic helper.
func TestDispatchKind_String(t *testing.T) {
	cases := []struct {
		k    DispatchKind
		want string
	}{
		{KindDispatch, "Dispatch"},
		{KindDispatchNow, "DispatchNow"},
		{KindDispatchAsync, "DispatchAsync"},
		{KindDispatchAfter, "DispatchAfter"},
		{KindUntil, "Until"},
		{DispatchKind(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Fatalf("DispatchKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}

// TestBuffer_InstallBufferNestedReturnsChild verifies that calling
// InstallBuffer twice on the same prepared ctx returns a child handle
// anchored to the parent (savepoint semantics) without overwriting the
// holder slot.
func TestBuffer_InstallBufferNestedReturnsChild(t *testing.T) {
	ctx := PrepareBuffer(context.Background())
	parent, releaseParent := InstallBuffer(ctx, func(e BufferedEvent) error { return nil })
	defer releaseParent()
	child, releaseChild := InstallBuffer(ctx, nil)
	defer releaseChild()

	if child == parent {
		t.Fatal("nested InstallBuffer returned parent handle, want child")
	}
	if Buffer(ctx) != parent {
		t.Fatal("Buffer(ctx) lookup did not return parent during nested scope")
	}
	_ = Buffer(ctx).Dispatch(context.Background(), "a")
	_ = Buffer(ctx).Dispatch(context.Background(), "b")
	child.Drop() // savepoint rollback
	if parent.Pending() != 0 {
		t.Fatalf("parent.Pending after child Drop = %d, want 0", parent.Pending())
	}
}

// TestBuffer_FakeDispatcher_Integration verifies that flushing routes
// through a FakeDispatcher records the events as if they were dispatched
// directly (the assertion API stays usable for tx-buffered events).
func TestBuffer_FakeDispatcher_Integration(t *testing.T) {
	fake := NewFakeDispatcher()
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		return fake.Dispatch(context.Background(), e.Event())
	})
	_ = buf.Dispatch(context.Background(), &BaseEvent{EventName: "thing.happened"})
	// Pre-flush the fake should have nothing.
	if err := fake.AssertNothingDispatched(); err != nil {
		t.Fatalf("fake had events before flush: %v", err)
	}
	if err := buf.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := fake.AssertDispatched(&BaseEvent{}, nil); err != nil {
		t.Fatalf("AssertDispatched: %v", err)
	}
}

// TestBuffer_Concurrent verifies independent contexts yield independent
// buffers under -race. Each goroutine prepares its own ctx so the holder
// slots are isolated (mirrors the per-request ctx pattern in real code).
func TestBuffer_Concurrent(t *testing.T) {
	const goroutines = 16
	const eventsPer = 32

	var totalFlushed atomic.Int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			ctx := PrepareBuffer(context.Background())
			buf, release := InstallBuffer(ctx, func(e BufferedEvent) error {
				totalFlushed.Add(1)
				return nil
			})
			defer release()
			for i := 0; i < eventsPer; i++ {
				_ = Buffer(ctx).Dispatch(context.Background(), i)
			}
			if buf.Pending() != eventsPer {
				t.Errorf("g=%d Pending = %d, want %d", g, buf.Pending(), eventsPer)
			}
			if err := buf.Flush(); err != nil {
				t.Errorf("g=%d Flush: %v", g, err)
			}
		}(g)
	}
	wg.Wait()
	if got := totalFlushed.Load(); got != int64(goroutines*eventsPer) {
		t.Fatalf("totalFlushed = %d, want %d", got, goroutines*eventsPer)
	}
}

// TestBuffer_PanicInListenerAfterFlush verifies that a panicking flush
// callback does not corrupt buffer state (the buffer transitions to the
// flushed terminal state via the panic-recovery defer in Flush).
// The panic is recovered here because the user's flush callback is
// outside the framework's recovery scope; the test asserts the buffer
// itself remains in a consistent terminal state.
func TestBuffer_PanicInListenerAfterFlush(t *testing.T) {
	var calls int
	_, buf := WithBuffer(context.Background(), func(e BufferedEvent) error {
		calls++
		panic("listener boom")
	})
	_ = buf.Dispatch(context.Background(), "a")

	func() {
		defer func() { _ = recover() }()
		_ = buf.Flush()
	}()
	if calls != 1 {
		t.Fatalf("flushFn calls = %d, want 1", calls)
	}
	if buf.Pending() != 0 {
		t.Fatalf("Pending after panicking Flush = %d, want 0", buf.Pending())
	}
	// Buffer must accept Drop / further Flush calls cleanly.
	buf.Drop()
	if err := buf.Flush(); err != nil {
		t.Fatalf("post-panic Flush: %v", err)
	}
}
