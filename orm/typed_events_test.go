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
	if got := e.Name(); got != "query.executed" {
		t.Fatalf("QueryExecuted.Name = %q, want %q", got, "query.executed")
	}
}

// TestEventInterface_QueryFailed ensures QueryFailed satisfies Event.
func TestEventInterface_QueryFailed(t *testing.T) {
	var e Event = &QueryFailed{}
	if got := e.Name(); got != "query.failed" {
		t.Fatalf("QueryFailed.Name = %q, want %q", got, "query.failed")
	}
}

// TestManager_SetEventDispatcher_ReceivesEvent verifies the dispatcher
// receives an orm.Event and can recover the typed payload via assertion.
func TestManager_SetEventDispatcher_ReceivesEvent(t *testing.T) {
	var (
		mu       sync.Mutex
		received any
	)

	m := &Manager{}
	m.SetEventDispatcher(func(_ context.Context, e any) error {
		mu.Lock()
		defer mu.Unlock()
		received = e
		return nil
	})

	m.dispatchEvent(context.Background(), &QueryExecuted{
		Context:    context.Background(),
		SQL:        "SELECT 1",
		Connection: "sqlite",
	})

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("dispatcher did not receive event")
	}
	q, ok := received.(*QueryExecuted)
	if !ok {
		t.Fatalf("received event is not *QueryExecuted: %T", received)
	}
	if q.Name() != "query.executed" {
		t.Errorf("Name = %q, want %q", q.Name(), "query.executed")
	}
}

// TestManager_SetEventDispatcher_NilClears verifies that passing nil clears
// the dispatcher (no-op dispatch afterwards).
func TestManager_SetEventDispatcher_NilClears(t *testing.T) {
	m := &Manager{}
	m.SetEventDispatcher(func(context.Context, any) error { return nil })
	m.SetEventDispatcher(nil)
	// Must not panic.
	m.dispatchEvent(context.Background(), &QueryExecuted{SQL: "SELECT 1"})
}
