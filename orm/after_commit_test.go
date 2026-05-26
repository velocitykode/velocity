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

func (l *afterCommitTestListener) ShouldQueue() bool               { return false }
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

func (l *inlineTestListener) ShouldQueue() bool { return false }

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
