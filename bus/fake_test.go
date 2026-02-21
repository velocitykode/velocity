package bus

import (
	"testing"
)

func TestFakeBus_Dispatch(t *testing.T) {
	f := NewFakeBus()

	err := f.Dispatch(createUser{Name: "Alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dispatched := f.GetDispatched()
	if len(dispatched) != 1 {
		t.Fatalf("expected 1 dispatched, got %d", len(dispatched))
	}
	if dispatched[0].(createUser).Name != "Alice" {
		t.Fatalf("wrong command recorded")
	}
}

func TestFakeBus_AssertDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should fail when nothing dispatched
	if err := f.AssertDispatched(createUser{}); err == nil {
		t.Fatal("expected error when command not dispatched")
	}

	// Should pass after dispatch
	f.Dispatch(createUser{Name: "Bob"})
	if err := f.AssertDispatched(createUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_AssertDispatchedTimes(t *testing.T) {
	f := NewFakeBus()

	// Zero dispatches
	if err := f.AssertDispatchedTimes(createUser{}, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Wrong count should fail
	f.Dispatch(createUser{})
	if err := f.AssertDispatchedTimes(createUser{}, 2); err == nil {
		t.Fatal("expected error for wrong count")
	}

	// Correct count should pass
	f.Dispatch(createUser{})
	if err := f.AssertDispatchedTimes(createUser{}, 2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Different type should be 0
	if err := f.AssertDispatchedTimes(deleteUser{}, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_AssertNotDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass when nothing dispatched
	if err := f.AssertNotDispatched(createUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fail after dispatch
	f.Dispatch(createUser{})
	if err := f.AssertNotDispatched(createUser{}); err == nil {
		t.Fatal("expected error when command was dispatched")
	}

	// Different type should still pass
	if err := f.AssertNotDispatched(deleteUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_AssertNothingDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should pass with no dispatches
	if err := f.AssertNothingDispatched(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should fail after any dispatch
	f.Dispatch(createUser{})
	if err := f.AssertNothingDispatched(); err == nil {
		t.Fatal("expected error when commands were dispatched")
	}
}

func TestFakeBus_AssertAsyncDispatched(t *testing.T) {
	f := NewFakeBus()

	// Should fail when nothing async dispatched
	if err := f.AssertAsyncDispatched(createUser{}); err == nil {
		t.Fatal("expected error when command not async dispatched")
	}

	// Sync dispatch should not count
	f.Dispatch(createUser{})
	if err := f.AssertAsyncDispatched(createUser{}); err == nil {
		t.Fatal("expected error: sync dispatch should not count as async")
	}

	// Should pass after async dispatch
	f.DispatchAsync(createUser{})
	if err := f.AssertAsyncDispatched(createUser{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFakeBus_ClearDispatched(t *testing.T) {
	f := NewFakeBus()

	f.Dispatch(createUser{})
	f.DispatchAsync(deleteUser{})

	f.ClearDispatched()

	if err := f.AssertNothingDispatched(); err != nil {
		t.Fatalf("expected nothing dispatched after clear: %v", err)
	}

	dispatched := f.GetDispatched()
	if len(dispatched) != 0 {
		t.Fatalf("expected empty dispatched list after clear, got %d", len(dispatched))
	}

	// Async should also be cleared
	if err := f.AssertAsyncDispatched(deleteUser{}); err == nil {
		t.Fatal("expected async dispatched to be cleared")
	}
}

func TestFakeBus_ImplementsDispatcher(t *testing.T) {
	var _ Dispatcher = NewFakeBus()
}

func TestFakeBus_GetDispatched_ReturnsCopy(t *testing.T) {
	f := NewFakeBus()
	f.Dispatch(createUser{Name: "A"})

	d1 := f.GetDispatched()
	d2 := f.GetDispatched()

	// Modifying one should not affect the other
	d1[0] = createUser{Name: "modified"}
	if d2[0].(createUser).Name != "A" {
		t.Fatal("GetDispatched should return a copy, not the original slice")
	}
}
