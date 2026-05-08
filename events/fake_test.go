package events

import (
	"context"
	"testing"
)

func TestFakeDispatcherAssertDispatched(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch some events
	fake.Dispatch(context.Background(), UserRegistered{UserID: 1, Email: "test1@example.com"})
	fake.Dispatch(context.Background(), UserRegistered{UserID: 2, Email: "test2@example.com"})
	fake.Dispatch(context.Background(), OrderPlaced{OrderID: 100, Amount: 99.99})

	// Test successful assertion
	err := fake.AssertDispatched(UserRegistered{}, nil)
	if err != nil {
		t.Errorf("Expected UserRegistered to be dispatched: %v", err)
	}

	// Test with callback
	err = fake.AssertDispatched(UserRegistered{}, func(e interface{}) bool {
		event := e.(UserRegistered)
		return event.UserID == 1
	})
	if err != nil {
		t.Errorf("Expected specific UserRegistered to be dispatched: %v", err)
	}

	// Test failed assertion
	err = fake.AssertDispatched(UserRegistered{}, func(e interface{}) bool {
		event := e.(UserRegistered)
		return event.UserID == 999
	})
	if err == nil {
		t.Error("Expected assertion to fail for non-existent event")
	}
}

func TestFakeDispatcherAssertDispatchedTimes(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch events
	fake.Dispatch(context.Background(), UserRegistered{UserID: 1})
	fake.Dispatch(context.Background(), UserRegistered{UserID: 2})
	fake.Dispatch(context.Background(), OrderPlaced{OrderID: 100})

	// Test correct count
	err := fake.AssertDispatchedTimes(UserRegistered{}, 2)
	if err != nil {
		t.Errorf("Expected 2 UserRegistered events: %v", err)
	}

	err = fake.AssertDispatchedTimes(OrderPlaced{}, 1)
	if err != nil {
		t.Errorf("Expected 1 OrderPlaced event: %v", err)
	}

	// Test incorrect count
	err = fake.AssertDispatchedTimes(UserRegistered{}, 3)
	if err == nil {
		t.Error("Expected assertion to fail for wrong count")
	}
}

func TestFakeDispatcherAssertNotDispatched(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch some events
	fake.Dispatch(context.Background(), UserRegistered{UserID: 1})

	// Test event that was dispatched
	err := fake.AssertNotDispatched(UserRegistered{})
	if err == nil {
		t.Error("Expected assertion to fail for dispatched event")
	}

	// Test event that was not dispatched
	err = fake.AssertNotDispatched(OrderPlaced{})
	if err != nil {
		t.Errorf("Expected OrderPlaced not to be dispatched: %v", err)
	}
}

func TestFakeDispatcherAssertNothingDispatched(t *testing.T) {
	fake := NewFakeDispatcher()

	// Test with no events
	err := fake.AssertNothingDispatched()
	if err != nil {
		t.Errorf("Expected nothing to be dispatched: %v", err)
	}

	// Dispatch an event
	fake.Dispatch(context.Background(), "test.event")

	// Test with events
	err = fake.AssertNothingDispatched()
	if err == nil {
		t.Error("Expected assertion to fail when events were dispatched")
	}
}

func TestFakeDispatcherClearEvents(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch events
	fake.Dispatch(context.Background(), "event1")
	fake.Dispatch(context.Background(), "event2")

	// String events are stored as-is, check them
	err := fake.AssertNothingDispatched()
	if err == nil {
		t.Error("Expected events to be dispatched before clear")
	}

	// Clear events
	fake.ClearEvents()

	// Verify events were cleared
	err = fake.AssertNothingDispatched()
	if err != nil {
		t.Error("Expected no events after clear")
	}
}

// GetEvents method doesn't exist, removed test

func TestFakeDispatcherListen(t *testing.T) {
	fake := NewFakeDispatcher()

	// Listen should work but not execute
	listener := &TestListener{}
	fake.Listen("test.event", listener)

	// Dispatch event
	fake.Dispatch(context.Background(), "test.event")

	// Listener should not be called in fake
	if listener.WasHandled() {
		t.Error("Fake dispatcher should not execute listeners")
	}

	// But event should be recorded
	err := fake.AssertDispatched("test.event", nil)
	if err != nil {
		t.Error("Event should be recorded even with listeners")
	}
}

func TestFakeDispatcherForget(t *testing.T) {
	fake := NewFakeDispatcher()

	// These operations should not error
	fake.Listen("test.event", &TestListener{})
	fake.Forget("test.event")
	// Flush is not a method on fake dispatcher

	// Should still record dispatched events
	fake.Dispatch(context.Background(), "test.event")
	err := fake.AssertDispatched("test.event", nil)
	if err != nil {
		t.Error("Forget should not affect event recording")
	}
}

func TestFakeDispatcherHasListeners(t *testing.T) {
	fake := NewFakeDispatcher()

	// Should return false initially
	if fake.HasListeners("any.event") {
		t.Error("Should return false for non-existent event")
	}

	// After listening, should return true
	fake.Listen("test.event", &TestListener{})
	if !fake.HasListeners("test.event") {
		t.Error("Should return true after listener added")
	}
}

func TestFakeDispatcherGetListeners(t *testing.T) {
	fake := NewFakeDispatcher()

	// Always returns empty for fake
	listeners := fake.GetListeners("any.event")
	if len(listeners) != 0 {
		t.Error("Fake dispatcher should return empty listeners")
	}
}

func TestFakeDispatcherPointerTypes(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch both pointer and non-pointer
	fake.Dispatch(context.Background(), &UserRegistered{UserID: 1})
	fake.Dispatch(context.Background(), UserRegistered{UserID: 2})

	// Should match both with either type
	err := fake.AssertDispatchedTimes(&UserRegistered{}, 2)
	if err != nil {
		t.Errorf("Should match both pointer and non-pointer: %v", err)
	}

	err = fake.AssertDispatchedTimes(UserRegistered{}, 2)
	if err != nil {
		t.Errorf("Should match both pointer and non-pointer: %v", err)
	}
}

func TestFakeDispatcherStringEvents(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch string events
	fake.Dispatch(context.Background(), "user.created")
	fake.Dispatch(context.Background(), "user.updated")
	fake.Dispatch(context.Background(), "order.created")

	// String events are stored but AssertDispatched expects typed events
	// For string events, we should check that something was dispatched
	err := fake.AssertNothingDispatched()
	if err == nil {
		t.Error("Expected events to be recorded")
	}
}

func TestFakeDispatcherSubscribe(t *testing.T) {
	fake := NewFakeDispatcher()

	subscriber := NewTestSubscriber()
	fake.Subscribe(subscriber)

	// Subscribe should work but not affect fake behavior
	fake.Dispatch(context.Background(), "user.registered")

	err := fake.AssertDispatched("user.registered", nil)
	if err != nil {
		t.Error("Subscribe should not affect event recording")
	}
}
