package orm

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// txTraceCollector records dispatched ORM events for assertions.
type txTraceCollector struct {
	mu     sync.Mutex
	events []any
}

func (c *txTraceCollector) dispatch(_ context.Context, ev any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

func (c *txTraceCollector) snapshot() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]any, len(c.events))
	copy(out, c.events)
	return out
}

// TestTransaction_StatementsParentUnderTxSpan covers the core trace shape:
// three statements inside a tx all parent under the same tx span, and one
// TransactionExecuted event reports that span with Statements: 3.
func TestTransaction_StatementsParentUnderTxSpan(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	collector := &txTraceCollector{}
	m.SetEventDispatcher(collector.dispatch)

	err := m.Transaction(context.Background(), func(txCtx context.Context) error {
		for i, name := range []string{"alice", "bob", "carol"} {
			if _, err := (User{}).Create(txCtx, map[string]any{
				"name":  name,
				"email": name + "@example.com",
				"age":   20 + i,
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	// Statement events are delivered asynchronously via the event pump;
	// force delivery before asserting on them.
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	var (
		queryEvents []*QueryExecuted
		txEvent     *TransactionExecuted
	)
	for _, ev := range collector.snapshot() {
		switch e := ev.(type) {
		case *QueryExecuted:
			queryEvents = append(queryEvents, e)
		case *TransactionExecuted:
			if txEvent != nil {
				t.Fatalf("multiple TransactionExecuted events; second seen: %+v", e)
			}
			txEvent = e
		}
	}

	if txEvent == nil {
		t.Fatal("TransactionExecuted event not dispatched")
	}
	if txEvent.Error != "" {
		t.Errorf("TransactionExecuted Error: got %q want empty", txEvent.Error)
	}
	if txEvent.SpanID == "" {
		t.Fatal("TransactionExecuted SpanID empty")
	}
	if txEvent.Statements != len(queryEvents) {
		t.Errorf("Statements: got %d, but %d QueryExecuted events fired",
			txEvent.Statements, len(queryEvents))
	}
	if txEvent.Statements < 3 {
		t.Errorf("Statements: got %d want >= 3", txEvent.Statements)
	}
	if txEvent.Connection == "" {
		t.Error("TransactionExecuted Connection empty")
	}

	// Every query under the tx must parent under the tx span and share the trace.
	for i, qe := range queryEvents {
		if qe.ParentID != txEvent.SpanID {
			t.Errorf("query[%d] ParentID: got %q want %q (tx span)", i, qe.ParentID, txEvent.SpanID)
		}
		if qe.TraceID != txEvent.TraceID {
			t.Errorf("query[%d] TraceID: got %q want %q", i, qe.TraceID, txEvent.TraceID)
		}
	}
}

// TestTransaction_RollbackEmitsTransactionExecutedWithError covers the
// rollback path: closure returns an error → TransactionExecuted fires with
// Error populated.
func TestTransaction_RollbackEmitsTransactionExecutedWithError(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	collector := &txTraceCollector{}
	m.SetEventDispatcher(collector.dispatch)

	sentinel := errors.New("rollback me")
	err := m.Transaction(context.Background(), func(txCtx context.Context) error {
		if _, err := (User{}).Create(txCtx, map[string]any{
			"name":  "ghost",
			"email": "ghost@example.com",
			"age":   1,
		}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction returned %v, want sentinel", err)
	}

	var txEvent *TransactionExecuted
	for _, ev := range collector.snapshot() {
		if e, ok := ev.(*TransactionExecuted); ok {
			txEvent = e
		}
	}
	if txEvent == nil {
		t.Fatal("TransactionExecuted event not dispatched on rollback")
	}
	if txEvent.Error == "" {
		t.Error("TransactionExecuted Error empty on rollback; want closure error message")
	}
	if txEvent.Error != sentinel.Error() {
		t.Errorf("TransactionExecuted Error: got %q want %q", txEvent.Error, sentinel.Error())
	}
}

// TestTransaction_NestedInnerSpanParentUnderOuter covers nested-tx
// behaviour: the inner Transaction call mints its own span and inner
// statements parent under the inner span, while the inner span itself is
// parented under the outer span.
func TestTransaction_NestedInnerSpanParentUnderOuter(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	collector := &txTraceCollector{}
	m.SetEventDispatcher(collector.dispatch)

	// Inner closure is empty so SQL semantics don't matter; only the trace
	// shape is asserted (nested span parents under the outer tx span).
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		if err := m.Transaction(outerCtx, func(innerCtx context.Context) error {
			return nil
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	var txEvents []*TransactionExecuted
	for _, ev := range collector.snapshot() {
		if e, ok := ev.(*TransactionExecuted); ok {
			txEvents = append(txEvents, e)
		}
	}

	if len(txEvents) != 2 {
		t.Fatalf("got %d TransactionExecuted events; want 2 (outer + inner)", len(txEvents))
	}

	// Inner fires first because it commits before the outer wrapper returns.
	inner, outer := txEvents[0], txEvents[1]
	if inner.SpanID == outer.SpanID {
		t.Fatal("inner and outer tx spans collapsed onto the same SpanID")
	}
	if inner.ParentID != outer.SpanID {
		t.Errorf("inner ParentID: got %q want %q (outer span)", inner.ParentID, outer.SpanID)
	}
	if inner.TraceID != outer.TraceID {
		t.Errorf("inner TraceID: got %q want %q", inner.TraceID, outer.TraceID)
	}
}

// TestTransaction_TopLevelUntracedCallerMintsTrace covers the entry point a
// CLI / cron / unit test hits: ctx has no incoming trace, so Manager.Transaction
// must mint one. TransactionExecuted ships with a generated TraceID, a non-empty
// SpanID (the tx span), and an empty ParentID (no caller span existed).
func TestTransaction_TopLevelUntracedCallerMintsTrace(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	collector := &txTraceCollector{}
	m.SetEventDispatcher(collector.dispatch)

	if err := m.Transaction(context.Background(), func(txCtx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	var txEvent *TransactionExecuted
	for _, ev := range collector.snapshot() {
		if e, ok := ev.(*TransactionExecuted); ok {
			txEvent = e
		}
	}
	if txEvent == nil {
		t.Fatal("TransactionExecuted not dispatched")
	}
	if txEvent.TraceID == "" {
		t.Error("TraceID empty: top-level caller without incoming trace must mint one")
	}
	if txEvent.SpanID == "" {
		t.Error("SpanID empty: tx span must be non-empty")
	}
	if txEvent.ParentID != "" {
		t.Errorf("ParentID: got %q want empty (no caller span)", txEvent.ParentID)
	}
}
