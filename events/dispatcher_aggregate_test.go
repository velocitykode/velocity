package events

import (
	"errors"
	"testing"
)

// aggCountListener records every Handle invocation and optionally returns err.
type aggCountListener struct {
	calls int
	err   error
}

func (l *aggCountListener) Handle(event interface{}) error {
	l.calls++
	return l.err
}
func (l *aggCountListener) ShouldQueue() bool { return false }

// TestDispatch_AggregatesListenerErrors covers Task 6c: errors from every
// listener are joined so one failure does not mask later ones.
func TestDispatch_AggregatesListenerErrors(t *testing.T) {
	d := NewDispatcher()

	e1 := errors.New("velocity/test: first")
	e2 := errors.New("velocity/test: second")

	l1 := &aggCountListener{err: e1}
	l2 := &aggCountListener{err: e2}
	l3 := &aggCountListener{} // ok

	d.Listen("my.event", l1)
	d.Listen("my.event", l2)
	d.Listen("my.event", l3)

	err := d.Dispatch("my.event")
	if err == nil {
		t.Fatal("expected aggregated error, got nil")
	}
	if !errors.Is(err, e1) || !errors.Is(err, e2) {
		t.Errorf("expected both e1 and e2 via errors.Is, got %v", err)
	}

	// All three listeners must run — no short-circuit.
	if l1.calls != 1 || l2.calls != 1 || l3.calls != 1 {
		t.Fatalf("listener call counts = %d/%d/%d, want 1/1/1", l1.calls, l2.calls, l3.calls)
	}
}
