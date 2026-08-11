package orm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/events"
)

// afterCommitTestListener implements events.ShouldDispatchAfterCommit so
// the dispatcher gate inside Dispatch defers it to FireAfterCommit.
type afterCommitTestListener struct {
	defer_      bool
	invocations atomic.Int32
}

func (l *afterCommitTestListener) Handle(ctx context.Context, event interface{}) error {
	l.invocations.Add(1)
	return nil
}

func (l *afterCommitTestListener) Async() bool                     { return false }
func (l *afterCommitTestListener) ShouldDispatchAfterCommit() bool { return l.defer_ }

// inlineTestListener is the behaviour control: no opt-in interface, must
// fire inline regardless of after-commit queue state.
type inlineTestListener struct {
	invocations atomic.Int32
}

func (l *inlineTestListener) Handle(ctx context.Context, event interface{}) error {
	l.invocations.Add(1)
	return nil
}

func (l *inlineTestListener) Async() bool { return false }

// TestTransaction_AfterCommitListener_FiresOnCommit asserts the
// integration M-48 happy path: a listener opting in via
// ShouldDispatchAfterCommit fires once the surrounding Transaction
// commits, not inside fn.
func TestTransaction_AfterCommitListener_FiresOnCommit(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		// Inside the tx the listener must NOT have fired even though
		// Dispatch returned.
		if err := d.Dispatch(ctx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		if got := listener.invocations.Load(); got != 0 {
			t.Errorf("listener fired %d times inside tx; want 0", got)
		}
		// The queue must be present on the per-tx ctx.
		if !events.HasAfterCommitQueue(ctx) {
			t.Error("HasAfterCommitQueue returned false inside Transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction returned %v", err)
	}

	// After commit, the listener fires exactly once.
	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times after commit; want 1", got)
	}
}

// TestTransaction_AfterCommitListener_DroppedOnRollback pins the M-48
// invariant: a listener opting in must NEVER fire when the surrounding
// transaction rolls back. This is the phantom-side-effect class the
// finding closes.
func TestTransaction_AfterCommitListener_DroppedOnRollback(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	rollback := errors.New("force rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		if err := d.Dispatch(ctx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("Transaction err = %v, want %v", err, rollback)
	}

	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times after rollback; want 0", got)
	}
}

// TestTransaction_AfterCommitListener_DroppedOnPanic asserts the panic
// path also drops queued listeners. Without this, a handler that panics
// past the orm boundary would leak side effects from listeners that had
// queued before the panic.
func TestTransaction_AfterCommitListener_DroppedOnPanic(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	defer func() {
		if p := recover(); p == nil {
			t.Fatal("Transaction did not re-panic")
		}
		if got := listener.invocations.Load(); got != 0 {
			t.Fatalf("listener fired %d times after panic; want 0", got)
		}
	}()

	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
		if err := d.Dispatch(ctx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		panic("force unwind")
	})
}

// TestTransaction_NonOptInListener_FiresInline pins the non-regression
// guarantee: listeners that do NOT implement ShouldDispatchAfterCommit
// fire inline inside the tx, same as before M-48.
func TestTransaction_NonOptInListener_FiresInline(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &inlineTestListener{}
	d.Listen("user.created", listener)

	insideTxFired := int32(-1)
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		if err := d.Dispatch(ctx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		insideTxFired = listener.invocations.Load()
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction returned %v", err)
	}

	if insideTxFired != 1 {
		t.Fatalf("non-opt-in listener fired %d times inside tx; want 1", insideTxFired)
	}
	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("non-opt-in listener total %d; want 1", got)
	}
}

// TestTransaction_OptInListener_FiresInlineOutsideTx asserts that the
// same listener fires inline when there is no surrounding transaction.
// The opt-in interface is a transaction-aware behaviour selector, not a
// permanent "always defer" marker.
func TestTransaction_OptInListener_FiresInlineOutsideTx(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	// No Transaction wrapping the Dispatch.
	if err := d.Dispatch(context.Background(), "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("opt-in listener fired %d times outside tx; want 1 (no queue installed)", got)
	}
}

// TestTransaction_NestedTransaction_OuterCommitFiresListeners asserts the
// nested-tx contract: a listener dispatched inside an INNER Transaction
// must wait for the OUTER Transaction's commit, not the inner one. This
// matches the buffer's savepoint semantics: inner commit is a savepoint
// release, not a durable boundary.
func TestTransaction_NestedTransaction_OuterCommitFiresListeners(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	var firedAfterInner int32
	var firedAfterOuter int32
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := m.Transaction(outerCtx, func(innerCtx context.Context) error {
			if err := d.Dispatch(innerCtx, "user.created"); err != nil {
				t.Fatalf("Dispatch returned error: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("inner Transaction returned %v", err)
		}
		// Inner commit returned: the listener must STILL not have fired
		// because the outer Transaction owns the after-commit drain.
		firedAfterInner = listener.invocations.Load()
		return nil
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}
	firedAfterOuter = listener.invocations.Load()

	if firedAfterInner != 0 {
		t.Errorf("listener fired %d times after inner commit; want 0 (outer owns drain)", firedAfterInner)
	}
	if firedAfterOuter != 1 {
		t.Errorf("listener fired %d times after outer commit; want 1", firedAfterOuter)
	}
}

// TestTransaction_NestedTransaction_OuterRollbackDropsListeners asserts
// that an inner-committed but outer-rolled-back transaction drops the
// listener. The savepoint semantics mirror the existing buffer: only the
// outermost commit makes side effects durable.
func TestTransaction_NestedTransaction_OuterRollbackDropsListeners(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	rollback := errors.New("force outer rollback")
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := m.Transaction(outerCtx, func(innerCtx context.Context) error {
			if err := d.Dispatch(innerCtx, "user.created"); err != nil {
				t.Fatalf("Dispatch returned error: %v", err)
			}
			return nil // inner "commits" successfully
		}); err != nil {
			t.Fatalf("inner Transaction returned %v", err)
		}
		return rollback // outer rolls back; inner work must drop
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("Transaction err = %v, want %v", err, rollback)
	}

	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times after outer rollback; want 0", got)
	}
}

// TestTransaction_NestedInnerRollback_DropsOnlyInnerWork closes the M-48
// nested-rollback leak. Without the AfterCommitHandle baseline/truncate
// shape, an inner Transaction that enqueues a listener and then returns
// an error would leave that listener on the outer queue: the outer
// Transaction would later commit and fire a listener from a logically
// rolled-back savepoint. This pins:
//
//   - inner-enqueued listener does NOT fire when inner rolls back, even
//     if the outer body swallows the error and commits;
//   - outer-enqueued listener (registered before the inner block) DOES
//     fire on outer commit;
//   - outer-enqueued listener registered AFTER the inner rollback ALSO
//     fires (the truncate only removes inner's contribution).
func TestTransaction_NestedInnerRollback_DropsOnlyInnerWork(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	outerBefore := &afterCommitTestListener{defer_: true}
	inner := &afterCommitTestListener{defer_: true}
	outerAfter := &afterCommitTestListener{defer_: true}
	d.Listen("outer.before", outerBefore)
	d.Listen("inner.event", inner)
	d.Listen("outer.after", outerAfter)

	innerRollback := errors.New("force inner rollback")
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		// Outer enqueues A (outer.before) before opening the inner.
		if err := d.Dispatch(outerCtx, "outer.before"); err != nil {
			t.Fatalf("outer.before Dispatch: %v", err)
		}

		// Inner enqueues B (inner.event) and rolls back.
		innerErr := m.Transaction(outerCtx, func(innerCtx context.Context) error {
			if err := d.Dispatch(innerCtx, "inner.event"); err != nil {
				t.Fatalf("inner.event Dispatch: %v", err)
			}
			return innerRollback
		})
		if !errors.Is(innerErr, innerRollback) {
			t.Fatalf("inner Transaction err = %v; want innerRollback", innerErr)
		}

		// Outer SWALLOWS the inner error and continues; enqueues C.
		if err := d.Dispatch(outerCtx, "outer.after"); err != nil {
			t.Fatalf("outer.after Dispatch: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}

	// outer.before fired (outer-enqueued, before inner block).
	if got := outerBefore.invocations.Load(); got != 1 {
		t.Errorf("outer.before fired %d times; want 1", got)
	}
	// inner.event did NOT fire (inner-enqueued, inner rolled back).
	if got := inner.invocations.Load(); got != 0 {
		t.Errorf("inner.event fired %d times after inner rollback; want 0", got)
	}
	// outer.after fired (outer-enqueued, after inner block).
	if got := outerAfter.invocations.Load(); got != 1 {
		t.Errorf("outer.after fired %d times; want 1", got)
	}
}

// TestTransaction_NestedInnerPanic_DropsOnlyInnerWork mirrors the
// rollback case for panics: an inner Transaction that enqueues a
// listener and then panics must NOT leave that listener on the outer
// queue when the outer body recovers and commits.
func TestTransaction_NestedInnerPanic_DropsOnlyInnerWork(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	outerL := &afterCommitTestListener{defer_: true}
	innerL := &afterCommitTestListener{defer_: true}
	d.Listen("outer.event", outerL)
	d.Listen("inner.event", innerL)

	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := d.Dispatch(outerCtx, "outer.event"); err != nil {
			t.Fatalf("outer.event Dispatch: %v", err)
		}

		// Recover the inner panic at the outer body level so the outer
		// can still commit.
		func() {
			defer func() {
				_ = recover() // swallow inner panic
			}()
			_ = m.Transaction(outerCtx, func(innerCtx context.Context) error {
				if err := d.Dispatch(innerCtx, "inner.event"); err != nil {
					t.Fatalf("inner.event Dispatch: %v", err)
				}
				panic("force unwind in inner")
			})
		}()

		return nil
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}

	if got := outerL.invocations.Load(); got != 1 {
		t.Errorf("outer.event fired %d times; want 1", got)
	}
	if got := innerL.invocations.Load(); got != 0 {
		t.Errorf("inner.event fired %d times after inner panic; want 0", got)
	}
}

// TestTransaction_NestedInnerCommit_AllListenersFire pins the "happy
// nested" path: inner commits cleanly (returns nil), outer commits
// cleanly. Both outer-enqueued and inner-enqueued listeners fire.
// Inner commit is a no-op at the handle level; the outer commit owns
// the drain.
func TestTransaction_NestedInnerCommit_AllListenersFire(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	outerL := &afterCommitTestListener{defer_: true}
	innerL := &afterCommitTestListener{defer_: true}
	d.Listen("outer.event", outerL)
	d.Listen("inner.event", innerL)

	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := d.Dispatch(outerCtx, "outer.event"); err != nil {
			t.Fatalf("outer.event Dispatch: %v", err)
		}
		return m.Transaction(outerCtx, func(innerCtx context.Context) error {
			return d.Dispatch(innerCtx, "inner.event")
		})
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}

	if got := outerL.invocations.Load(); got != 1 {
		t.Errorf("outer.event fired %d times; want 1", got)
	}
	if got := innerL.invocations.Load(); got != 1 {
		t.Errorf("inner.event fired %d times; want 1", got)
	}
}

// TestTransaction_RespectsPrepareAfterCommit_FiresOnCommit is the canonical
// M-48 F2 regression: a caller that explicitly calls PrepareAfterCommit
// before opening a Transaction MUST see ShouldDispatchAfterCommit
// listeners fire on commit. Before the claim/owner refactor, the prepared
// queue was treated as already-nested by InstallAfterCommitQueue and the
// Transaction wrapper never drained it: every queued listener went
// unfired on commit, silently breaking outbox enqueues, read-model
// updates, and any side effect the caller assumed was deferred to commit.
func TestTransaction_RespectsPrepareAfterCommit_FiresOnCommit(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	ctx := events.PrepareAfterCommit(context.Background())

	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if err := d.Dispatch(txCtx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		if got := listener.invocations.Load(); got != 0 {
			t.Errorf("listener fired %d times inside tx; want 0", got)
		}
		if !events.HasAfterCommitQueue(txCtx) {
			t.Error("HasAfterCommitQueue returned false inside Transaction on a prepared ctx")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction returned %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times after commit; want 1 (queue was orphaned before claim fix)", got)
	}
}

// TestTransaction_RespectsPrepareAfterCommit_DropsOnRollback mirrors the
// fires-on-commit test for the rollback path: when the caller prepared
// the queue upfront and the Transaction body returns an error, the
// listener MUST NOT fire. Without the claim flip the Transaction never
// owned the queue, so DropAfterCommit was a no-op and the tasks would
// have sat in the queue forever (still unfired but reachable via any
// later code holding the ctx).
func TestTransaction_RespectsPrepareAfterCommit_DropsOnRollback(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	ctx := events.PrepareAfterCommit(context.Background())

	rollback := errors.New("force rollback")
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if err := d.Dispatch(txCtx, "user.created"); err != nil {
			t.Fatalf("Dispatch returned error: %v", err)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatalf("Transaction err = %v, want %v", err, rollback)
	}

	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times after rollback on prepared ctx; want 0", got)
	}

	// And the queue must be fully drained / terminal now: a later
	// Dispatch on the same ctx fires inline, it does not stack on a
	// dead queue.
	if events.HasAfterCommitQueue(ctx) {
		t.Fatal("HasAfterCommitQueue returned true after rollback drained the owner")
	}
}

// TestTransaction_TripleNested_InnerRollback_MidAndOuterCommit pins the
// three-level case: outer -> mid -> inner. Inner rolls back; mid
// continues and commits; outer commits. Mid- and outer-enqueued
// listeners fire; inner-enqueued listener does not. This verifies the
// baseline mechanism cascades correctly: the mid handle's baseline is
// the outer's enqueue count, the inner handle's baseline is the mid's
// post-mid-enqueue count, and inner truncate touches only inner.
func TestTransaction_TripleNested_InnerRollback_MidAndOuterCommit(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	outerL := &afterCommitTestListener{defer_: true}
	midL := &afterCommitTestListener{defer_: true}
	innerL := &afterCommitTestListener{defer_: true}
	d.Listen("outer.event", outerL)
	d.Listen("mid.event", midL)
	d.Listen("inner.event", innerL)

	innerRollback := errors.New("force inner rollback")
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := d.Dispatch(outerCtx, "outer.event"); err != nil {
			return err
		}
		return m.Transaction(outerCtx, func(midCtx context.Context) error {
			if err := d.Dispatch(midCtx, "mid.event"); err != nil {
				return err
			}
			innerErr := m.Transaction(midCtx, func(innerCtx context.Context) error {
				if err := d.Dispatch(innerCtx, "inner.event"); err != nil {
					return err
				}
				return innerRollback
			})
			if !errors.Is(innerErr, innerRollback) {
				t.Fatalf("inner Transaction err = %v; want innerRollback", innerErr)
			}
			// Mid swallows inner error and commits.
			return nil
		})
	})
	if err != nil {
		t.Fatalf("outer Transaction returned %v", err)
	}

	if got := outerL.invocations.Load(); got != 1 {
		t.Errorf("outer.event fired %d times; want 1", got)
	}
	if got := midL.invocations.Load(); got != 1 {
		t.Errorf("mid.event fired %d times; want 1", got)
	}
	if got := innerL.invocations.Load(); got != 0 {
		t.Errorf("inner.event fired %d times after inner rollback; want 0", got)
	}
}

// TestTransaction_SavepointRelease_NoAfterCommitOnOuterRollback pins the
// transaction-rollback test-helper shape: the outer "transaction" is a raw
// *sql.Tx threaded via WithTxContext (no Manager.Transaction, so no event /
// after-commit holders in ctx). A Manager.Transaction inside it nests as a
// savepoint. Releasing that savepoint is NOT a durable commit, so after-commit
// listeners must NOT fire - the real outer tx is rolled back by the helper.
// Without tying side-effect ownership to the real outer boundary, the inner
// scope would wrongly own the after-commit queue and fire on RELEASE SAVEPOINT.
func TestTransaction_SavepointRelease_NoAfterCommitOnOuterRollback(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	d := events.NewDispatcher()
	listener := &afterCommitTestListener{defer_: true}
	d.Listen("user.created", listener)

	tx, err := m.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	ctx := WithTxContext(context.Background(), tx)

	if err := m.Transaction(ctx, func(innerCtx context.Context) error {
		return d.Dispatch(innerCtx, "user.created")
	}); err != nil {
		t.Fatalf("nested Transaction: %v", err)
	}

	// Savepoint released, but nothing is durable yet.
	if got := listener.invocations.Load(); got != 0 {
		t.Errorf("listener fired %d times on savepoint release; want 0 (not a durable commit)", got)
	}

	// The helper rolls the real outer tx back: side effects must never fire.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := listener.invocations.Load(); got != 0 {
		t.Errorf("listener fired %d times after outer rollback; want 0", got)
	}
}
