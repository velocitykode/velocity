package orm

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestDispatchQueryExecuted_FiresOnBulkUpdate is the regression test for the
// "dispatchQueryExecuted is a no-op" bug. Before the fix the function body
// dropped every event on the floor; after the fix the event must reach the
// configured dispatcher whenever a query terminal runs.
func TestDispatchQueryExecuted_FiresOnBulkUpdate(t *testing.T) {
	manager := setupConvenienceTests(t)

	var (
		mu    sync.Mutex
		seen  []*QueryExecuted
		other int
	)
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*QueryExecuted); ok {
			seen = append(seen, q)
			return nil
		}
		other++
		return nil
	})

	seedUser(t, manager, "alice", "a@example.com", 20)
	seedUser(t, manager, "bob", "b@example.com", 30)

	mu.Lock()
	seen = nil
	mu.Unlock()

	rows, err := Model[TestUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 99,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatalf("expected at least one QueryExecuted, got 0")
	}
	var found *QueryExecuted
	for _, q := range seen {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q.SQL)), "UPDATE") {
			found = q
			break
		}
	}
	if found == nil {
		t.Fatalf("no QueryExecuted matched UPDATE; saw %d events", len(seen))
	}
	if found.RowsAffected != 2 {
		t.Errorf("RowsAffected = %d, want 2", found.RowsAffected)
	}
	if found.Connection != "sqlite" {
		t.Errorf("Connection = %q, want sqlite", found.Connection)
	}
	if found.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", found.Duration)
	}
	if found.File == "" || found.Line == 0 {
		t.Errorf("caller info missing: file=%q line=%d", found.File, found.Line)
	}
	// Caller frame must point at the application caller, not the framework.
	// Pre-fix the skip count was off-by-one and File/Line landed on
	// query.go's bulkUpdate body instead of the test source.
	if !strings.HasSuffix(found.File, "_test.go") {
		t.Errorf("caller frame did not unwind to the test: file=%q (expected suffix _test.go)", found.File)
	}
	if strings.Contains(found.File, "query.go") {
		t.Errorf("caller frame stuck at framework: file=%q", found.File)
	}
}

// TestDispatchQueryExecuted_PreSelectCallerFrame locks in the
// caller-info skip threading on the bulk-hook pre-SELECT. The
// pre-SELECT runs from inside selectPrimaryKeys (called by
// bulkPrepareHooks, called by bulkUpdate, called by Update).
// Without correctly threaded callerSkip the captured File/Line
// would point at bulkPrepareHooks (velocity-internal) instead of
// the application test source.
func TestDispatchQueryExecuted_PreSelectCallerFrame(t *testing.T) {
	manager := setupBulkTestSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	var (
		mu   sync.Mutex
		seen []*QueryExecuted
	)
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := ev.(*QueryExecuted); ok {
			seen = append(seen, q)
		}
		return nil
	})

	_, uerr := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 88,
	})
	if uerr != nil {
		t.Fatalf("Update: %v", uerr)
	}

	mu.Lock()
	defer mu.Unlock()
	var preSelect *QueryExecuted
	for _, q := range seen {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q.SQL)), "SELECT") {
			preSelect = q
			break
		}
	}
	if preSelect == nil {
		t.Fatalf("no SELECT QueryExecuted captured; saw %d events", len(seen))
	}
	if !strings.HasSuffix(preSelect.File, "_test.go") {
		t.Errorf("pre-SELECT caller frame did not unwind to the test: file=%q (expected suffix _test.go)", preSelect.File)
	}
	if strings.Contains(preSelect.File, "bulk.go") || strings.Contains(preSelect.File, "query.go") {
		t.Errorf("pre-SELECT caller frame stuck at framework: file=%q", preSelect.File)
	}
}
