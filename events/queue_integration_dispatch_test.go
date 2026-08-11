package events

import (
	"context"
	"errors"
	"testing"
)

// orderingPriorityListener records the order in which Dispatch invokes it,
// tagged with a label, and reports a fixed priority.
type orderingPriorityListener struct {
	label    string
	priority int
	order    *[]string
}

func (l *orderingPriorityListener) Handle(_ context.Context, _ interface{}) error {
	*l.order = append(*l.order, l.label)
	return nil
}
func (l *orderingPriorityListener) Async() bool   { return false }
func (l *orderingPriorityListener) Priority() int { return l.priority }

// TestPriorityDispatcher_Dispatch_InvokesInPriorityOrder is the B9 regression.
// PriorityDispatcher inherits Dispatch from QueueIntegratedDispatcher; before
// the listenersFor hook, that inherited Dispatch bound getListenersForEvent
// statically to the promoted DefaultDispatcher method, so the priority sort
// was silently skipped and listeners fired in registration order. This test
// dispatches through Dispatch (not getListenersForEvent directly) and asserts
// the sync invocation order follows priority high-to-low.
func TestPriorityDispatcher_Dispatch_InvokesInPriorityOrder(t *testing.T) {
	d := NewPriorityDispatcher()

	var order []string
	low := &orderingPriorityListener{label: "low", priority: 10, order: &order}
	high := &orderingPriorityListener{label: "high", priority: 100, order: &order}
	med := &orderingPriorityListener{label: "med", priority: 50, order: &order}

	// Register in non-priority order.
	d.Listen("evt", low)
	d.Listen("evt", high)
	d.Listen("evt", med)

	if err := d.Dispatch(context.Background(), "evt"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	want := []string{"high", "med", "low"}
	if len(order) != len(want) {
		t.Fatalf("invocation order = %v; want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("invocation order = %v; want %v", order, want)
		}
	}
}

// TestQueueIntegratedDispatcher_AfterCommitListener_FiresOnCommit asserts the
// after-commit alignment: a ShouldDispatchAfterCommit listener registered on a
// QueueIntegratedDispatcher and dispatched inside a PrepareAfterCommit ctx is
// deferred onto the after-commit queue and fires only when FireAfterCommit
// runs (mirrors DefaultDispatcher). Before the alignment the listener fired
// synchronously inside the transaction.
func TestQueueIntegratedDispatcher_AfterCommitListener_FiresOnCommit(t *testing.T) {
	d := NewQueueIntegratedDispatcher()
	listener := &afterCommitListener{defer_: true}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Must not have fired yet: the dispatch deferred it.
	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times before commit; want 0", got)
	}
	if pending := PendingAfterCommit(ctx); pending != 1 {
		t.Fatalf("PendingAfterCommit = %d; want 1", pending)
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times after commit; want 1", got)
	}
}

// TestQueueIntegratedDispatcher_AggregatesListenerErrors asserts the errors.Join
// alignment: two failing listeners both execute and both errors surface through
// the joined error via errors.Is. The pre-alignment first-error-return would
// have stopped after the first listener, masking the second.
func TestQueueIntegratedDispatcher_AggregatesListenerErrors(t *testing.T) {
	d := NewQueueIntegratedDispatcher()

	e1 := errors.New("velocity/test: first")
	e2 := errors.New("velocity/test: second")

	l1 := &aggCountListener{err: e1}
	l2 := &aggCountListener{err: e2}

	d.Listen("my.event", l1)
	d.Listen("my.event", l2)

	err := d.Dispatch(context.Background(), "my.event")
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Errorf("expected both e1 and e2 via errors.Is, got %v", err)
	}
	// Both listeners must run; no short-circuit on the first error.
	if l1.calls != 1 || l2.calls != 1 {
		t.Fatalf("listener call counts = %d/%d, want 1/1", l1.calls, l2.calls)
	}
}
