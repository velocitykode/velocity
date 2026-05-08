package orm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// ---------------------------------------------------------------------------
// Test fixture: returning-capable wrapper around the sqlite driver.
//
// Real SQLite 3.35+ supports RETURNING on UPDATE / DELETE, and the
// mattn/go-sqlite3 build used by the test infra ships with a recent enough
// libsqlite3. The wrapper keeps DriverName() == "sqlite" so the timestamp
// sentinel and other driver-name-keyed branches behave correctly, while
// publishing a Grammar that implements drivers.ReturningGrammar so the
// bulk-hook path opts into the atomic capture branch. This exercises the
// RETURNING runtime path (QueryContext + scanReturnedIDs) end-to-end
// against a real database, locking in the new code that the unit-only
// grammar tests can't reach.
// ---------------------------------------------------------------------------

// returningTestDriver wraps a real driver and overrides Grammar() to
// surface a returningTestGrammar (which implements ReturningGrammar).
// All other methods delegate to the inner driver unchanged.
type returningTestDriver struct {
	drivers.Driver
	inner drivers.QueryGrammar
}

func newReturningTestDriver(d drivers.Driver) *returningTestDriver {
	return &returningTestDriver{Driver: d, inner: d.Grammar()}
}

func (d *returningTestDriver) Grammar() drivers.QueryGrammar {
	return &returningTestGrammar{QueryGrammar: d.inner}
}

// returningTestGrammar wraps a base QueryGrammar and adds the
// ReturningGrammar capability. CompileUpdateReturning / CompileDeleteReturning
// reuse the base UPDATE / DELETE compiler and append RETURNING <pk>, which
// is valid SQLite 3.35+ syntax.
type returningTestGrammar struct {
	drivers.QueryGrammar
}

func (g *returningTestGrammar) CompileUpdateReturning(table string, values map[string]any, conditions []drivers.Condition, pkCol string) (string, []any) {
	sqlStr, args := g.QueryGrammar.CompileUpdate(table, values, conditions)
	return sqlStr + " RETURNING " + g.QueryGrammar.QuoteIdentifier(pkCol), args
}

func (g *returningTestGrammar) CompileDeleteReturning(table string, conditions []drivers.Condition, pkCol string) (string, []any) {
	sqlStr, args := g.QueryGrammar.CompileDelete(table, conditions)
	return sqlStr + " RETURNING " + g.QueryGrammar.QuoteIdentifier(pkCol), args
}

// installReturningWrapper swaps the manager's default driver for a
// returning-capable wrapper so the bulk-hook path resolves to the
// RETURNING branch. Same package as Manager so the unexported field is
// reachable; the lock guard mirrors the rest of the manager code.
func installReturningWrapper(t *testing.T, m *Manager) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.defaultDriver == nil {
		t.Fatal("manager has no default driver to wrap")
	}
	m.defaultDriver = newReturningTestDriver(m.defaultDriver)
}

// setupReturningSchema is setupBulkTestSchema followed by the wrapper
// install. Returned manager routes every Query through the
// ReturningGrammar branch.
func setupReturningSchema(t *testing.T) *Manager {
	t.Helper()
	m := setupBulkTestSchema(t)
	installReturningWrapper(t, m)
	return m
}

// ---------------------------------------------------------------------------
// Runtime tests for the RETURNING branch.
// ---------------------------------------------------------------------------

func TestBulkAfterCommit_Returning_FiresWithIDs(t *testing.T) {
	setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 50,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("BulkAfterCommit calls=%d, want 1", len(calls))
	}
	if calls[0].Op != BulkOpUpdate {
		t.Errorf("Op=%q, want %q", calls[0].Op, BulkOpUpdate)
	}
	if len(calls[0].IDs) != 3 {
		t.Errorf("ids=%v, want 3 ids captured via RETURNING", calls[0].IDs)
	}
	if int64(len(calls[0].IDs)) != rows {
		t.Errorf("len(ids)=%d does not match rowsAffected=%d", len(calls[0].IDs), rows)
	}
}

func TestBulkAfterCommit_Returning_FiresOnZeroRowMatch(t *testing.T) {
	setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 9999).Update(context.Background(), map[string]any{
		"age": 1,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 0 {
		t.Fatalf("rowsAffected=%d, want 0 on zero-row match", rows)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("BulkAfterCommit calls=%d, want 1 (zero-row writes still fire)", len(calls))
	}
	if len(calls[0].IDs) != 0 {
		t.Errorf("ids=%v, want empty slice on zero-row match", calls[0].IDs)
	}
}

func TestBulkAfterCommit_Returning_NotFiredOnExecError(t *testing.T) {
	setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	// Force a unique-constraint violation on email. The RETURNING
	// statement issues QueryContext; the driver returns an error
	// before any rows are scanned, plan.invoke must NOT fire.
	_, err := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"email": "collision@example.com",
	})
	if err == nil {
		t.Fatalf("expected unique-constraint error, got nil")
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("BulkAfterCommit fired despite write error: calls=%d", got)
	}
}

func TestBulkAfterCommit_Returning_SoftDeleteOp(t *testing.T) {
	setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(calls))
	}
	if calls[0].Op != BulkOpDelete {
		t.Errorf("Op=%q, want %q (RETURNING soft-delete must dispatch BulkOpDelete)", calls[0].Op, BulkOpDelete)
	}
	if len(calls[0].IDs) != 3 {
		t.Errorf("ids count=%d, want 3", len(calls[0].IDs))
	}
}

func TestBulkAfterCommit_Returning_ForceDelete(t *testing.T) {
	setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).ForceDelete(context.Background())
	if err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(calls))
	}
	if calls[0].Op != BulkOpForceDelete {
		t.Errorf("Op=%q, want %q", calls[0].Op, BulkOpForceDelete)
	}
	if len(calls[0].IDs) != 3 {
		t.Errorf("ids count=%d, want 3", len(calls[0].IDs))
	}
}

func TestBulkAfterCommit_Returning_FiresAfterTxCommit(t *testing.T) {
	manager := setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	err := manager.Transaction(PrepareTxCallbacks(context.Background()), func(ctx context.Context) error {
		_, uerr := Model[BulkUser]{}.Where("age > ?", 0).Update(ctx, map[string]any{
			"age": 7,
		})
		if uerr != nil {
			return uerr
		}
		// Hook must NOT have fired pre-commit.
		if got := len(rec.snapshot()); got != 0 {
			t.Errorf("hook fired before commit: calls=%d", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Fatalf("post-commit hook calls=%d, want 1", got)
	}
}

func TestBulkAfterCommit_Returning_NotFiredOnTxRollback(t *testing.T) {
	manager := setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	sentinel := errors.New("rollback")
	err := manager.Transaction(PrepareTxCallbacks(context.Background()), func(ctx context.Context) error {
		_, uerr := Model[BulkUser]{}.Where("age > ?", 0).Update(ctx, map[string]any{
			"age": 1,
		})
		if uerr != nil {
			return uerr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction err=%v, want sentinel", err)
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("BulkAfterCommit fired despite rollback: calls=%d", got)
	}
}

// TestBulkAfterCommit_Returning_QueryExecutedFiresOnce locks in the
// "single event per statement" guarantee: the RETURNING path must NOT
// emit the pre-SELECT QueryExecuted event that the fallback path
// emits, because RETURNING IS the write.
func TestBulkAfterCommit_Returning_QueryExecutedFiresOnce(t *testing.T) {
	manager := setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	var queryEvents []*QueryExecuted
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		if q, ok := ev.(*QueryExecuted); ok {
			queryEvents = append(queryEvents, q)
		}
		return nil
	})
	t.Cleanup(func() { manager.SetEventDispatcher(nil) })

	_, uerr := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 33,
	})
	if uerr != nil {
		t.Fatalf("Update: %v", uerr)
	}

	var writes int
	var preselects int
	for _, q := range queryEvents {
		upper := strings.ToUpper(strings.TrimSpace(q.SQL))
		switch {
		case strings.HasPrefix(upper, "UPDATE") || strings.HasPrefix(upper, "DELETE"):
			writes++
		case strings.HasPrefix(upper, "SELECT"):
			preselects++
		}
	}
	if writes != 1 {
		t.Errorf("write QueryExecuted events=%d, want 1", writes)
	}
	if preselects != 0 {
		t.Errorf("RETURNING path must not emit pre-SELECT events, got %d", preselects)
	}
}
