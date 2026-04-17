package orm

import (
	"context"
	"sync"
	"testing"
)

// TestEventInterface_QueryExecuted ensures QueryExecuted satisfies the
// new Event interface and returns the documented event name.
func TestEventInterface_QueryExecuted(t *testing.T) {
	var e Event = &QueryExecuted{}
	if got := e.EventName(); got != "query.executed" {
		t.Fatalf("QueryExecuted.EventName = %q, want %q", got, "query.executed")
	}
}

// TestEventInterface_QueryFailed ensures QueryFailed satisfies Event.
func TestEventInterface_QueryFailed(t *testing.T) {
	var e Event = &QueryFailed{}
	if got := e.EventName(); got != "query.failed" {
		t.Fatalf("QueryFailed.EventName = %q, want %q", got, "query.failed")
	}
}

// TestManager_SetTypedEventDispatcher_ReceivesTypedEvent verifies the
// typed dispatcher receives an orm.Event and can call EventName() without
// a type assertion.
func TestManager_SetTypedEventDispatcher_ReceivesTypedEvent(t *testing.T) {
	var (
		mu       sync.Mutex
		received Event
	)

	m := &Manager{}
	m.SetTypedEventDispatcher(func(e Event) error {
		mu.Lock()
		defer mu.Unlock()
		received = e
		return nil
	})

	m.dispatchEvent(&QueryExecuted{
		Context:    context.Background(),
		SQL:        "SELECT 1",
		Connection: "sqlite",
	})

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("typed dispatcher did not receive event")
	}
	if received.EventName() != "query.executed" {
		t.Errorf("received.EventName() = %q, want %q", received.EventName(), "query.executed")
	}
}

// TestManager_SetEventDispatcher_BackwardCompat verifies the deprecated
// untyped setter still works; it must adapt the legacy
// func(event interface{}) error shape to the typed dispatcher internally.
func TestManager_SetEventDispatcher_BackwardCompat(t *testing.T) {
	var (
		mu       sync.Mutex
		received interface{}
	)

	m := &Manager{}
	m.SetEventDispatcher(func(e interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		received = e
		return nil
	})

	m.dispatchEvent(&QueryExecuted{
		Context:    context.Background(),
		SQL:        "SELECT 1",
		Connection: "sqlite",
	})

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("untyped dispatcher did not receive event")
	}
	q, ok := received.(*QueryExecuted)
	if !ok {
		t.Fatalf("received event is not *QueryExecuted: %T", received)
	}
	if q.EventName() != "query.executed" {
		t.Errorf("EventName = %q, want %q", q.EventName(), "query.executed")
	}
}

// TestManager_SetEventDispatcher_NilClears verifies that passing nil to
// the deprecated setter clears the dispatcher (no-op dispatch afterwards).
func TestManager_SetEventDispatcher_NilClears(t *testing.T) {
	m := &Manager{}
	m.SetTypedEventDispatcher(func(Event) error { return nil })
	m.SetEventDispatcher(nil)
	// Must not panic.
	m.dispatchEvent(&QueryExecuted{SQL: "SELECT 1"})
}
