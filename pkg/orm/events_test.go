package orm

import (
	"context"
	"sync"
	"testing"
)

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

func (c *testEventCollector) getEvents() []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]interface{}, len(c.events))
	copy(result, c.events)
	return result
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

func setupTestDispatcher() *testEventCollector {
	collector := newTestEventCollector()
	SetEventDispatcher(collector.dispatch)
	return collector
}

func teardownTestDispatcher() {
	SetEventDispatcher(nil)
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

func TestDispatchQueryExecuted(t *testing.T) {
	collector := setupTestDispatcher()
	defer teardownTestDispatcher()

	ctx := context.Background()
	sql := "SELECT * FROM users WHERE id = ?"
	bindings := []any{1}

	dispatchQueryExecuted(ctx, sql, bindings, 100, 5, "mysql", 1)

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

	ctx := context.WithValue(context.Background(), "test", "value")
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

func TestQueryExecutedEventWithContext(t *testing.T) {
	collector := setupTestDispatcher()
	defer teardownTestDispatcher()

	ctx := context.WithValue(context.Background(), "request_id", "test-123")

	dispatchQueryExecuted(ctx, "SELECT 1", nil, 50, 1, "sqlite", 1)

	// Verify context was included in event
	event := collector.findEvent(func(e interface{}) bool {
		if q, ok := e.(*QueryExecuted); ok {
			if q.Context == nil {
				return false
			}
			val := q.Context.Value("request_id")
			return val == "test-123"
		}
		return false
	})
	if event == nil {
		t.Error("QueryExecuted context not passed correctly")
	}
}

func TestQueryExecutedEventBindings(t *testing.T) {
	collector := setupTestDispatcher()
	defer teardownTestDispatcher()

	bindings := []any{1, "test", true, 3.14}
	dispatchQueryExecuted(context.Background(), "INSERT INTO test", bindings, 100, 1, "postgres", 1)

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

func TestQueryExecutedEventConnection(t *testing.T) {
	collector := setupTestDispatcher()
	defer teardownTestDispatcher()

	testCases := []string{"mysql", "postgres", "sqlite"}

	for _, conn := range testCases {
		collector.clear()
		dispatchQueryExecuted(context.Background(), "SELECT 1", nil, 10, 1, conn, 1)

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
