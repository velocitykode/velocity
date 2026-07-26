package orm

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// ---------------------------------------------------------------------------
// Test fixtures for WithBulkLock
//
// The hook surface around bulk Update/Delete/ForceDelete already runs through
// a hidden pre-SELECT on every non-RETURNING driver. WithBulkLock asks that
// pre-SELECT to compile with FOR UPDATE so concurrent writers block on the
// captured rows for the surrounding transaction's duration.
//
// Two observation channels back the assertions:
//
//   - QueryExecuted events (registered via Manager.SetEventDispatcher) capture
//     the final SQL string emitted by the driver. SQLite silently drops
//     FOR UPDATE because the grammar does not emit it, so SQL-string
//     assertions only run after we install a grammar shim that does.
//   - lockProbeGrammar records the LockForUpdate flag on every SelectQuery
//     passed to CompileSelect. This is the storage-layer truth, independent
//     of whether the driver eventually emits FOR UPDATE textually, so the
//     tests can assert intent regardless of SQLite's parse-and-drop policy.
// ---------------------------------------------------------------------------

// lockProbeDriver wraps a real driver and replaces its grammar with a
// lockProbeGrammar that records the LockForUpdate flag of every SELECT
// passed through it. All other Driver methods delegate to the inner driver
// unchanged. The wrapper installs the same way as returningTestDriver: the
// manager's defaultDriver field is swapped under the manager mutex.
type lockProbeDriver struct {
	drivers.Driver
	probe *lockProbeGrammar
}

// lockProbeGrammar wraps a base QueryGrammar and records the LockForUpdate
// flag on every CompileSelect invocation. The probe is a pure observer:
// the SQL it returns is exactly what the underlying grammar produced, so
// SQLite (which rejects FOR UPDATE syntactically) keeps executing
// unchanged. Tests assert on the recorded LockForUpdate flag instead of
// the textual SQL because that is the storage-layer truth, independent of
// dialect.
type lockProbeGrammar struct {
	drivers.QueryGrammar
	mu       sync.Mutex
	selects  []bool   // LockForUpdate value for each CompileSelect call
	rendered []string // SQL rendered for each CompileSelect call
}

func (g *lockProbeGrammar) CompileSelect(q *drivers.SelectQuery) (string, []any) {
	sqlStr, args := g.QueryGrammar.CompileSelect(q)
	g.mu.Lock()
	g.selects = append(g.selects, q.LockForUpdate)
	g.rendered = append(g.rendered, sqlStr)
	g.mu.Unlock()
	return sqlStr, args
}

func (g *lockProbeGrammar) lockedSelectCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	var n int
	for _, locked := range g.selects {
		if locked {
			n++
		}
	}
	return n
}

func (g *lockProbeGrammar) selectCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.selects)
}

func (g *lockProbeGrammar) sqlSnapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.rendered...)
}

// installLockProbe swaps the manager's default driver for a lockProbeDriver
// and returns the probe grammar so tests can inspect it. Mirrors the lock
// guard used by installReturningWrapper.
func installLockProbe(t *testing.T, m *Manager) *lockProbeGrammar {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.defaultDriver == nil {
		t.Fatal("manager has no default driver to wrap")
	}
	probe := &lockProbeGrammar{QueryGrammar: m.defaultDriver.Grammar()}
	m.defaultDriver = &lockProbeDriver{Driver: m.defaultDriver, probe: probe}
	return probe
}

func (d *lockProbeDriver) Grammar() drivers.QueryGrammar { return d.probe }

// ---------------------------------------------------------------------------
// WithBulkLock: pre-SELECT path on non-RETURNING drivers locks for update.
// ---------------------------------------------------------------------------

func TestWithBulkLock_PreSelectIncludesForUpdate(t *testing.T) {
	manager := setupBulkTestSchema(t)
	probe := installLockProbe(t, manager)

	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithBulkLock().Update(context.Background(), map[string]any{
		"age": 50,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	// Storage-layer assertion: at least one CompileSelect saw LockForUpdate.
	// This is dialect-independent; the grammar contract is "if the flag is
	// set, the dialect that supports row-level locking must emit FOR UPDATE
	// (verified by orm/drivers grammar tests for MySQL and PostgreSQL).
	// SQLite has no row-level locking and the flag is silently dropped.
	if probe.lockedSelectCount() == 0 {
		t.Fatalf("expected at least one CompileSelect with LockForUpdate=true, got 0 of %d", probe.selectCount())
	}

	if got := len(rec.snapshot()); got != 1 {
		t.Errorf("BulkAfterCommit calls=%d, want 1", got)
	}
}

// TestWithBulkLock_GrammarEmitsForUpdate complements
// TestWithBulkLock_PreSelectIncludesForUpdate with a textual check on the
// real production grammars. The runtime test runs against SQLite (which
// silently drops FOR UPDATE), so this unit-level test compiles a SELECT
// with LockForUpdate=true through MySQLGrammar and PostgresGrammar and
// asserts the rendered SQL carries the lock clause. Together the two
// tests prove the storage-layer flag flows from WithBulkLock through to
// the dialect grammars that actually emit FOR UPDATE.
func TestWithBulkLock_GrammarEmitsForUpdate(t *testing.T) {
	tests := []struct {
		name    string
		grammar drivers.QueryGrammar
	}{
		{"mysql", &drivers.MySQLGrammar{}},
		{"postgres", &drivers.PostgresGrammar{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sel := &drivers.SelectQuery{
				Table:         "bulk_users",
				Columns:       []string{"id"},
				LockForUpdate: true,
			}
			sqlStr, _ := tc.grammar.CompileSelect(sel)
			if !strings.Contains(strings.ToUpper(sqlStr), "FOR UPDATE") {
				t.Fatalf("grammar %s did not emit FOR UPDATE: %s", tc.name, sqlStr)
			}

			selUnlocked := &drivers.SelectQuery{
				Table:   "bulk_users",
				Columns: []string{"id"},
			}
			sqlUnlocked, _ := tc.grammar.CompileSelect(selUnlocked)
			if strings.Contains(strings.ToUpper(sqlUnlocked), "FOR UPDATE") {
				t.Fatalf("grammar %s emitted FOR UPDATE without LockForUpdate=true: %s", tc.name, sqlUnlocked)
			}
		})
	}
}

func TestWithBulkLock_OmittedHasNoForUpdate(t *testing.T) {
	manager := setupBulkTestSchema(t)
	probe := installLockProbe(t, manager)

	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 60,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	// Without WithBulkLock, no SELECT should have LockForUpdate set.
	if probe.lockedSelectCount() != 0 {
		t.Fatalf("expected 0 SELECTs with LockForUpdate=true, got %d (of %d total)", probe.lockedSelectCount(), probe.selectCount())
	}
	for _, sqlStr := range probe.sqlSnapshot() {
		if strings.Contains(strings.ToUpper(sqlStr), "FOR UPDATE") {
			t.Fatalf("expected NO FOR UPDATE in rendered SELECT without WithBulkLock, got: %s", sqlStr)
		}
	}
}

// ---------------------------------------------------------------------------
// WithBulkLock: silently ignored on the atomic RETURNING path.
// ---------------------------------------------------------------------------

// bulkLockSQLRecorder mirrors the QueryExecuted capture pattern used by
// TestBulkAfterCommit_Returning_QueryExecutedFiresOnce. It is parameter-less
// so each test owns its own buffer.
type bulkLockSQLRecorder struct {
	mu       sync.Mutex
	captured []string
}

func (r *bulkLockSQLRecorder) record(sqlStr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captured = append(r.captured, sqlStr)
}

func (r *bulkLockSQLRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.captured...)
}

func TestWithBulkLock_NoOpOnReturningPath(t *testing.T) {
	manager := setupReturningSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	disp := &bulkLockSQLRecorder{}
	manager.SetEventDispatcher(func(_ context.Context, ev any) error {
		if q, ok := ev.(*QueryExecuted); ok {
			disp.record(q.SQL)
		}
		return nil
	})
	t.Cleanup(func() { manager.SetEventDispatcher(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithBulkLock().Update(context.Background(), map[string]any{
		"age": 70,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}
	// Statement events are delivered asynchronously; force delivery before
	// inspecting what was recorded.
	if err := manager.FlushQueryEvents(context.Background()); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	queries := disp.snapshot()
	// The RETURNING path must not emit a separate SELECT (lock or otherwise);
	// the only statement should be the UPDATE ... RETURNING write.
	for _, q := range queries {
		upper := strings.ToUpper(strings.TrimSpace(q))
		if strings.HasPrefix(upper, "SELECT") {
			t.Fatalf("RETURNING path issued an extra SELECT, WithBulkLock should be a no-op there: %s", q)
		}
	}

	// And confirm the write actually used RETURNING, with no FOR UPDATE.
	var sawReturning bool
	for _, q := range queries {
		upper := strings.ToUpper(q)
		if strings.HasPrefix(strings.TrimSpace(upper), "UPDATE") {
			if strings.Contains(upper, "FOR UPDATE") {
				t.Fatalf("RETURNING UPDATE must not include FOR UPDATE: %s", q)
			}
			if strings.Contains(upper, "RETURNING") {
				sawReturning = true
			}
		}
	}
	if !sawReturning {
		t.Fatalf("expected an UPDATE ... RETURNING write, got: %v", queries)
	}

	// Hook still fires once via the atomic capture branch.
	if got := len(rec.snapshot()); got != 1 {
		t.Errorf("BulkAfterCommit calls=%d, want 1 (RETURNING path still captures)", got)
	}
}

// ---------------------------------------------------------------------------
// Propagation: through Clone and through the soft-delete delegation.
// ---------------------------------------------------------------------------

func TestWithBulkLock_PropagatesThroughClone(t *testing.T) {
	q := &Query[BulkUser]{}
	q = q.WithBulkLock()
	clone := q.Clone()
	if !clone.withBulkLock {
		t.Fatalf("Clone did not propagate withBulkLock")
	}
	// And the inverse: a clone of a non-locked query stays unlocked.
	plain := &Query[BulkUser]{}
	if plain.Clone().withBulkLock {
		t.Fatalf("Clone of unlocked query reported withBulkLock=true")
	}
}

func TestWithBulkLock_PropagatesThroughSoftDelete(t *testing.T) {
	manager := setupBulkTestSchema(t)
	probe := installLockProbe(t, manager)

	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	// Delete on a soft-deletable model delegates internally to bulkUpdate so
	// the bulk-hook capture path runs against the same SELECT-then-write
	// pipeline. WithBulkLock must reach that pre-SELECT.
	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithBulkLock().Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	if probe.lockedSelectCount() == 0 {
		t.Fatalf("expected at least one CompileSelect with LockForUpdate=true on soft-delete, got 0 of %d", probe.selectCount())
	}
	if got := len(rec.snapshot()); got != 1 {
		t.Errorf("BulkAfterCommit calls=%d, want 1", got)
	}
	if calls := rec.snapshot(); calls[0].Op != BulkOpDelete {
		t.Errorf("Op=%q, want %q", calls[0].Op, BulkOpDelete)
	}
}

// ---------------------------------------------------------------------------
// WithBulkLock + WithRowHooks: Tier C row-hydration SELECT also locks.
// ---------------------------------------------------------------------------

func TestWithRowHooks_PlusWithBulkLock(t *testing.T) {
	manager := setupBulkTestSchema(t)
	probe := installLockProbe(t, manager)

	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().WithBulkLock().Update(context.Background(), map[string]any{
		"age": 81,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	if probe.lockedSelectCount() == 0 {
		t.Fatalf("expected Tier C row-hydration SELECT to set LockForUpdate, got 0 of %d", probe.selectCount())
	}
	if got := rowRec.commitCount(); got != 3 {
		t.Errorf("AfterCommit fires=%d, want 3", got)
	}
}
