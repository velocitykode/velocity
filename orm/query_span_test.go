package orm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/trace"
)

// spanEventLog records every event a router and a manager dispatch.
type spanEventLog struct {
	mu     sync.Mutex
	events []any
}

func (l *spanEventLog) dispatch(_ context.Context, ev any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return nil
}

func (l *spanEventLog) snapshot() []any {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]any(nil), l.events...)
}

// TestQueryInsideHandler_IsChildOfRequestSpan drives a statement from a real
// router handler: the QueryExecuted event carries the request's trace id, a
// span id of its own and the request span as ParentID.
func TestQueryInsideHandler_IsChildOfRequestSpan(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	events := &spanEventLog{}
	m.SetEventDispatcher(events.dispatch)

	r := router.NewV2()
	r.SetEventDispatcher(events.dispatch)
	r.Get("/q", func(c *router.Context) error {
		if _, err := m.Exec(c.Request.Context(), "SELECT 1"); err != nil {
			return err
		}
		return c.String(http.StatusOK, "ok")
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/q", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("handler status = %d, want 200", rec.Code)
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	var handled *router.RequestHandled
	var query *QueryExecuted
	for _, ev := range events.snapshot() {
		switch e := ev.(type) {
		case *router.RequestHandled:
			handled = e
		case *QueryExecuted:
			if e.SQL == "SELECT 1" {
				query = e
			}
		}
	}
	if handled == nil || query == nil {
		t.Fatalf("missing events: RequestHandled=%v QueryExecuted=%v", handled != nil, query != nil)
	}
	if query.TraceID != handled.TraceID {
		t.Errorf("QueryExecuted.TraceID = %q, want the request's %q", query.TraceID, handled.TraceID)
	}
	if query.SpanID == "" || query.SpanID == handled.SpanID {
		t.Errorf("QueryExecuted.SpanID = %q, want its own span (request span %q)", query.SpanID, handled.SpanID)
	}
	if query.ParentID != handled.SpanID {
		t.Errorf("QueryExecuted.ParentID = %q, want the request span %q", query.ParentID, handled.SpanID)
	}
}

// TestQueryFailed_IsChildOfCallerSpan covers the failure event on the same
// path: its own span, the caller's span as parent.
func TestQueryFailed_IsChildOfCallerSpan(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	events := &spanEventLog{}
	m.SetEventDispatcher(events.dispatch)

	callerTrace, callerSpan := "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	ctx := trace.WithTrace(context.Background(), callerTrace, callerSpan)
	if _, err := m.Exec(ctx, "SELECT * FROM no_such_table"); err == nil {
		t.Fatal("expected the statement to fail")
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	var failed *QueryFailed
	for _, ev := range events.snapshot() {
		if e, ok := ev.(*QueryFailed); ok {
			failed = e
		}
	}
	if failed == nil {
		t.Fatal("QueryFailed not dispatched")
	}
	if failed.TraceID != callerTrace || failed.ParentID != callerSpan {
		t.Errorf("QueryFailed trace=%q parent=%q, want trace=%q parent=%q", failed.TraceID, failed.ParentID, callerTrace, callerSpan)
	}
	if failed.SpanID == "" || failed.SpanID == callerSpan {
		t.Errorf("QueryFailed.SpanID = %q, want its own span (caller span %q)", failed.SpanID, callerSpan)
	}
}

// TestTransaction_StatementsHaveOwnSpans pins that every statement under a
// transaction is its own span: distinct span ids, all parented under the tx
// span, none equal to it.
func TestTransaction_StatementsHaveOwnSpans(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	events := &spanEventLog{}
	m.SetEventDispatcher(events.dispatch)

	err := m.Transaction(context.Background(), func(txCtx context.Context) error {
		for _, stmt := range []string{"SELECT 1", "SELECT 2", "SELECT 3"} {
			if _, err := m.Exec(txCtx, stmt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	var tx *TransactionExecuted
	seen := map[string]bool{}
	var queries []*QueryExecuted
	for _, ev := range events.snapshot() {
		switch e := ev.(type) {
		case *TransactionExecuted:
			tx = e
		case *QueryExecuted:
			queries = append(queries, e)
		}
	}
	if tx == nil {
		t.Fatal("TransactionExecuted not dispatched")
	}
	if len(queries) < 3 {
		t.Fatalf("got %d QueryExecuted events, want >= 3", len(queries))
	}
	for i, q := range queries {
		if q.ParentID != tx.SpanID {
			t.Errorf("query[%d] ParentID = %q, want the tx span %q", i, q.ParentID, tx.SpanID)
		}
		if q.SpanID == "" || q.SpanID == tx.SpanID {
			t.Errorf("query[%d] SpanID = %q, want its own span (tx span %q)", i, q.SpanID, tx.SpanID)
		}
		if seen[q.SpanID] {
			t.Errorf("query[%d] SpanID %q shared with another statement", i, q.SpanID)
		}
		seen[q.SpanID] = true
	}
}
