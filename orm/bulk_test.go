package orm

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

// bulkHookRecorder accumulates BulkAfterCommit invocations from the test
// model receivers. The recorder lives on a package-level pointer because
// the hook receiver is a zero-valued model with no state of its own.
type bulkHookRecorder struct {
	mu    sync.Mutex
	calls []bulkHookCall
}

type bulkHookCall struct {
	IDs []any
	Op  BulkOp
}

func (r *bulkHookRecorder) record(ids []any, op BulkOp) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, bulkHookCall{IDs: append([]any(nil), ids...), Op: op})
}

func (r *bulkHookRecorder) snapshot() []bulkHookCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bulkHookCall(nil), r.calls...)
}

var (
	bulkHookActive *bulkHookRecorder
	bulkHookMu     sync.Mutex
)

func setBulkHookRecorder(r *bulkHookRecorder) {
	bulkHookMu.Lock()
	defer bulkHookMu.Unlock()
	bulkHookActive = r
}

// rowHookRecorder accumulates per-row AfterCommit / AfterRollback
// invocations for Tier C tests.
type rowHookRecorder struct {
	mu        sync.Mutex
	commits   []int64
	rollbacks []int64
}

func (r *rowHookRecorder) onCommit(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commits = append(r.commits, id)
}

func (r *rowHookRecorder) onRollback(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rollbacks = append(r.rollbacks, id)
}

func (r *rowHookRecorder) commitCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commits)
}

func (r *rowHookRecorder) rollbackCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rollbacks)
}

var (
	rowHookActive *rowHookRecorder
	rowHookMu     sync.Mutex
)

func setRowHookRecorder(r *rowHookRecorder) {
	rowHookMu.Lock()
	defer rowHookMu.Unlock()
	rowHookActive = r
}

// BulkUser is a soft-deletable model that implements BulkAfterCommitHook
// AND per-row AfterCommit/AfterRollback. Tests pick which surface to
// exercise via WithRowHooks and the active recorder.
type BulkUser struct {
	SoftDeleteModel[BulkUser]
	Name  string `orm:"column:name"`
	Email string `orm:"column:email;unique"`
	Age   int    `orm:"column:age"`
}

func (BulkUser) TableName() string {
	return "bulk_users"
}

func (BulkUser) BulkAfterCommit(_ context.Context, ids []any, op BulkOp) error {
	bulkHookMu.Lock()
	r := bulkHookActive
	bulkHookMu.Unlock()
	if r != nil {
		r.record(ids, op)
	}
	return nil
}

func (u *BulkUser) AfterCommit(_ context.Context) error {
	rowHookMu.Lock()
	r := rowHookActive
	rowHookMu.Unlock()
	if r != nil {
		r.onCommit(int64(u.ID))
	}
	return nil
}

func (u *BulkUser) AfterRollback(_ context.Context) error {
	rowHookMu.Lock()
	r := rowHookActive
	rowHookMu.Unlock()
	if r != nil {
		r.onRollback(int64(u.ID))
	}
	return nil
}

// CompositeKeyModel exists solely to test the composite-PK rejection
// path in pkColumnFor. BulkAfterCommitHook is implemented so Update
// reaches the pre-capture branch.
type CompositeKeyModel struct {
	OrgID  int64  `orm:"column:org_id;primaryKey"`
	UserID int64  `orm:"column:user_id;primaryKey"`
	Role   string `orm:"column:role"`
}

func (CompositeKeyModel) TableName() string                                          { return "composite_keys" }
func (CompositeKeyModel) BulkAfterCommit(_ context.Context, _ []any, _ BulkOp) error { return nil }

// setupBulkTestSchema creates the bulk_users table on manager and
// returns the manager (already wired as default by the caller). Three
// rows are seeded.
func setupBulkTestSchema(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	_, err := manager.DB().Exec(`
		CREATE TABLE bulk_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT UNIQUE NOT NULL,
			age INTEGER,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("create bulk_users: %v", err)
	}
	for i := 1; i <= 3; i++ {
		_, err := manager.DB().Exec(
			`INSERT INTO bulk_users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))`,
			"alice", // sentinel
			"a"+itoa(i)+"@example.com",
			20+i,
		)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Shutdown(context.Background())
	})
	return manager
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// Tier B: BulkAfterCommitHook
// ---------------------------------------------------------------------------

func TestBulkAfterCommit_FiresOnUpdate(t *testing.T) {
	setupBulkTestSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"age": 99,
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
		t.Errorf("ids=%v, want 3 ids", calls[0].IDs)
	}
}

func TestBulkAfterCommit_FiresOnSoftDelete(t *testing.T) {
	setupBulkTestSchema(t)
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
		t.Fatalf("BulkAfterCommit calls=%d, want 1", len(calls))
	}
	if calls[0].Op != BulkOpDelete {
		t.Errorf("Op=%q, want %q (soft delete must dispatch BulkOpDelete, not BulkOpUpdate)", calls[0].Op, BulkOpDelete)
	}
	if len(calls[0].IDs) != 3 {
		t.Errorf("ids=%v, want 3 ids", calls[0].IDs)
	}
}

func TestBulkAfterCommit_FiresOnForceDelete(t *testing.T) {
	setupBulkTestSchema(t)
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
		t.Fatalf("BulkAfterCommit calls=%d, want 1", len(calls))
	}
	if calls[0].Op != BulkOpForceDelete {
		t.Errorf("Op=%q, want %q", calls[0].Op, BulkOpForceDelete)
	}
}

func TestBulkAfterCommit_FiresAfterTxCommit(t *testing.T) {
	manager := setupBulkTestSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	err := manager.Transaction(PrepareTxCallbacks(context.Background()), func(ctx context.Context) error {
		_, err := Model[BulkUser]{}.Where("age > ?", 0).Update(ctx, map[string]any{
			"age": 50,
		})
		// Inside the tx the hook must NOT have fired yet: registration
		// is queued until commit.
		if err != nil {
			return err
		}
		if got := len(rec.snapshot()); got != 0 {
			t.Errorf("hook fired before commit: calls=%d", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("post-commit hook calls=%d, want 1", len(calls))
	}
}

func TestBulkAfterCommit_NotFiredOnTxRollback(t *testing.T) {
	manager := setupBulkTestSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	sentinel := errors.New("rollback sentinel")
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

func TestBulkAfterCommit_NotFiredOnExecError(t *testing.T) {
	setupBulkTestSchema(t)
	rec := &bulkHookRecorder{}
	setBulkHookRecorder(rec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	// Force a unique-constraint violation: set every row's email to
	// the same value. The bulk pre-SELECT succeeds, but the UPDATE
	// errors and the deferred hook closure must not fire.
	_, err := Model[BulkUser]{}.Where("age > ?", 0).Update(context.Background(), map[string]any{
		"email": "collision@example.com",
	})
	if err == nil {
		t.Fatalf("expected unique-constraint error, got nil")
	}
	if got := len(rec.snapshot()); got != 0 {
		t.Errorf("hook fired despite write error: calls=%d", got)
	}
}

func TestBulkAfterCommit_CompositePKError(t *testing.T) {
	manager := newTestManager(t)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	_, err := manager.DB().Exec(`
		CREATE TABLE composite_keys (
			org_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			role TEXT,
			PRIMARY KEY (org_id, user_id)
		)
	`)
	if err != nil {
		t.Fatalf("create composite_keys: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(ResetDefault)

	_, err = Model[CompositeKeyModel]{}.Where("org_id = ?", 1).Update(context.Background(), map[string]any{
		"role": "admin",
	})
	if err == nil {
		t.Fatalf("expected composite-PK error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Tier C: WithRowHooks
// ---------------------------------------------------------------------------

func TestWithRowHooks_FiresPerRowOnUpdate(t *testing.T) {
	setupBulkTestSchema(t)
	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	bulkRec := &bulkHookRecorder{}
	setBulkHookRecorder(bulkRec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().Update(context.Background(), map[string]any{
		"age": 77,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	if got := rowRec.commitCount(); got != 3 {
		t.Errorf("AfterCommit fires=%d, want 3", got)
	}
	if got := len(bulkRec.snapshot()); got != 0 {
		t.Errorf("BulkAfterCommit fired despite WithRowHooks: calls=%d", got)
	}
}

func TestWithRowHooks_FiresPerRowOnForceDelete(t *testing.T) {
	setupBulkTestSchema(t)
	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().ForceDelete(context.Background())
	if err != nil {
		t.Fatalf("ForceDelete: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}

	if got := rowRec.commitCount(); got != 3 {
		t.Errorf("AfterCommit fires=%d, want 3", got)
	}
}

func TestWithRowHooks_FiresAfterRollbackInTx(t *testing.T) {
	manager := setupBulkTestSchema(t)
	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	sentinel := errors.New("rollback")
	err := manager.Transaction(PrepareTxCallbacks(context.Background()), func(ctx context.Context) error {
		_, uerr := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().Update(ctx, map[string]any{
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

	if got := rowRec.rollbackCount(); got != 3 {
		t.Errorf("AfterRollback fires=%d, want 3", got)
	}
	if got := rowRec.commitCount(); got != 0 {
		t.Errorf("AfterCommit fired despite rollback: %d", got)
	}
}

func TestWithRowHooks_PropagatesThroughSoftDelete(t *testing.T) {
	setupBulkTestSchema(t)
	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().Delete(context.Background())
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}
	if got := rowRec.commitCount(); got != 3 {
		t.Errorf("AfterCommit fires=%d, want 3 (soft-delete via WithRowHooks must fan out per-row hooks)", got)
	}
}

// TestWithRowHooks_SuppressesBulkAfterCommit locks in the documented
// "WithRowHooks wins" contract: when a model implements both
// BulkAfterCommitHook and per-row AfterCommitHook AND the caller
// chains WithRowHooks, only per-row hooks fire. No double-fire.
func TestWithRowHooks_SuppressesBulkAfterCommit(t *testing.T) {
	setupBulkTestSchema(t)

	rowRec := &rowHookRecorder{}
	setRowHookRecorder(rowRec)
	t.Cleanup(func() { setRowHookRecorder(nil) })

	bulkRec := &bulkHookRecorder{}
	setBulkHookRecorder(bulkRec)
	t.Cleanup(func() { setBulkHookRecorder(nil) })

	rows, err := Model[BulkUser]{}.Where("age > ?", 0).WithRowHooks().Update(context.Background(), map[string]any{
		"age": 42,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if rows != 3 {
		t.Fatalf("rows=%d, want 3", rows)
	}
	if got := rowRec.commitCount(); got != 3 {
		t.Errorf("per-row AfterCommit fires=%d, want 3", got)
	}
	if got := len(bulkRec.snapshot()); got != 0 {
		t.Errorf("BulkAfterCommit fired despite WithRowHooks: calls=%d (no double-fire allowed)", got)
	}
}

// TestBulkAfterCommit_FiresOnZeroRowMatch verifies the zero-row
// contract: when the WHERE matches no rows the captured id slice is
// empty but the hook still fires once with the correct op. Listeners
// counting "writes that touched at least one row" must inspect the
// ids slice, not assume non-zero on dispatch.
func TestBulkAfterCommit_FiresOnZeroRowMatch(t *testing.T) {
	setupBulkTestSchema(t)
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
		t.Fatalf("rows=%d, want 0", rows)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("BulkAfterCommit calls=%d, want 1 (zero-row writes still fire)", len(calls))
	}
	if calls[0].Op != BulkOpUpdate {
		t.Errorf("Op=%q, want %q", calls[0].Op, BulkOpUpdate)
	}
	if len(calls[0].IDs) != 0 {
		t.Errorf("ids=%v, want empty slice on zero-row match", calls[0].IDs)
	}
}
