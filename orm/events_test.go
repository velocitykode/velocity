package orm

import (
	"context"
	"sync"
	"testing"
)

type testCtxKey string

// testEventCollector collects dispatched events for testing
type testEventCollector struct {
	mu     sync.Mutex
	events []interface{}
}

func newTestEventCollector() *testEventCollector {
	return &testEventCollector{
		events: make([]interface{}, 0),
	}
}

func (c *testEventCollector) dispatch(event interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	return nil
}

func (c *testEventCollector) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = make([]interface{}, 0)
}

func (c *testEventCollector) findEvent(predicate func(interface{}) bool) interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if predicate(e) {
			return e
		}
	}
	return nil
}

func TestQueryExecutedEventName(t *testing.T) {
	event := &QueryExecuted{}
	if got := event.Name(); got != "query.executed" {
		t.Errorf("QueryExecuted.Name() = %v, want %v", got, "query.executed")
	}
}

func TestCaptureCallerInfo(t *testing.T) {
	file, line := captureCallerInfo(1)

	if file == "unknown" {
		t.Error("Expected file to be captured, got 'unknown'")
	}

	if line == 0 {
		t.Error("Expected line to be captured, got 0")
	}

	if file == "" {
		t.Error("File should not be empty")
	}
}

func TestManagerDispatchEvent(t *testing.T) {
	collector := newTestEventCollector()
	m := &Manager{}
	m.SetEventDispatcher(collector.dispatch)

	ctx := context.Background()
	sql := "SELECT * FROM users WHERE id = ?"
	bindings := []any{1}

	m.dispatchEvent(&QueryExecuted{
		Context:      ctx,
		SQL:          sql,
		Bindings:     bindings,
		RowsAffected: 5,
		Connection:   "mysql",
		File:         "pkg/orm/events_test.go",
		Line:         90,
	})

	// Verify event was dispatched
	event := collector.findEvent(func(e interface{}) bool {
		if q, ok := e.(*QueryExecuted); ok {
			return q.SQL == sql &&
				len(q.Bindings) == 1 &&
				q.RowsAffected == 5 &&
				q.Connection == "mysql" &&
				q.File != "" &&
				q.Line > 0
		}
		return false
	})
	if event == nil {
		t.Error("QueryExecuted not dispatched correctly")
	}
}

func TestQueryWithContext(t *testing.T) {
	q := &Query[struct{}]{
		table:   "test",
		columns: []string{"*"},
	}

	ctx := context.WithValue(context.Background(), testCtxKey("test"), "value")
	q.WithContext(ctx)

	if q.ctx != ctx {
		t.Error("Context was not set correctly")
	}

	gotCtx := q.getContext()
	if gotCtx != ctx {
		t.Error("getContext did not return the set context")
	}
}

func TestQueryGetContextDefault(t *testing.T) {
	q := &Query[struct{}]{
		table:   "test",
		columns: []string{"*"},
	}

	ctx := q.getContext()
	if ctx == nil {
		t.Error("getContext should never return nil")
	}
}

func TestManagerDispatchEventWithContext(t *testing.T) {
	collector := newTestEventCollector()
	m := &Manager{}
	m.SetEventDispatcher(collector.dispatch)

	ctx := context.WithValue(context.Background(), testCtxKey("request_id"), "test-123")

	m.dispatchEvent(&QueryExecuted{
		Context:    ctx,
		SQL:        "SELECT 1",
		Connection: "sqlite",
	})

	// Verify context was included in event
	event := collector.findEvent(func(e interface{}) bool {
		if q, ok := e.(*QueryExecuted); ok {
			if q.Context == nil {
				return false
			}
			val := q.Context.Value(testCtxKey("request_id"))
			return val == "test-123"
		}
		return false
	})
	if event == nil {
		t.Error("QueryExecuted context not passed correctly")
	}
}

func TestManagerDispatchEventBindings(t *testing.T) {
	collector := newTestEventCollector()
	m := &Manager{}
	m.SetEventDispatcher(collector.dispatch)

	bindings := []any{1, "test", true, 3.14}
	m.dispatchEvent(&QueryExecuted{
		Context:    context.Background(),
		SQL:        "INSERT INTO test",
		Bindings:   bindings,
		Connection: "postgres",
	})

	// Verify bindings were captured
	event := collector.findEvent(func(e interface{}) bool {
		if q, ok := e.(*QueryExecuted); ok {
			if len(q.Bindings) != 4 {
				return false
			}
			return q.Bindings[0] == 1 &&
				q.Bindings[1] == "test" &&
				q.Bindings[2] == true &&
				q.Bindings[3] == 3.14
		}
		return false
	})
	if event == nil {
		t.Error("QueryExecuted bindings not captured correctly")
	}
}

func TestManagerDispatchEventConnection(t *testing.T) {
	collector := newTestEventCollector()
	m := &Manager{}
	m.SetEventDispatcher(collector.dispatch)

	testCases := []string{"mysql", "postgres", "sqlite"}

	for _, conn := range testCases {
		collector.clear()
		m.dispatchEvent(&QueryExecuted{
			Context:    context.Background(),
			SQL:        "SELECT 1",
			Connection: conn,
		})

		event := collector.findEvent(func(e interface{}) bool {
			if q, ok := e.(*QueryExecuted); ok {
				return q.Connection == conn
			}
			return false
		})
		if event == nil {
			t.Errorf("QueryExecuted connection %s not captured", conn)
		}
	}
}

func TestManagerDispatchEventNilDispatcher(t *testing.T) {
	m := &Manager{}
	// No dispatcher set - should not panic
	m.dispatchEvent(&QueryExecuted{
		Context: context.Background(),
		SQL:     "SELECT 1",
	})
}
