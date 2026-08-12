package orm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// txHookModel exercises BeforeCreate firing through the tx-bound path.
type txHookModel struct {
	Model[txHookModel]
	Name string `orm:"column:name"`
}

func (txHookModel) TableName() string { return "tx_hook_models" }
func (txHookModel) AssignableFields() []string {
	return []string{"name"}
}

// auditLog covers ImmutableModel[T] tx auto-enrollment. Append-only
// chain rows must land in the same tx as the writes they describe.
type auditLog struct {
	ImmutableModel[auditLog]
	Message string `orm:"column:message"`
}

func (auditLog) TableName() string          { return "audit_logs" }
func (auditLog) AssignableFields() []string { return []string{"message"} }

// hooksFiredOnTx records hook invocations for the tests below. Local
// package-level mutation is fine because the tests are single-threaded
// and reset the flags in setup.
var hooksFiredOnTx struct {
	beforeCreate bool
	afterCreate  bool
	beforeUpdate bool
	afterUpdate  bool
}

func (m *txHookModel) BeforeCreate() error {
	hooksFiredOnTx.beforeCreate = true
	return nil
}

func (m *txHookModel) AfterCreate() error {
	hooksFiredOnTx.afterCreate = true
	return nil
}

func (m *txHookModel) BeforeUpdate() error {
	hooksFiredOnTx.beforeUpdate = true
	return nil
}

func (m *txHookModel) AfterUpdate() error {
	hooksFiredOnTx.afterUpdate = true
	return nil
}

// setupTxTest installs a fresh sqlite-backed Manager as Default and
// builds the user/hook/audit tables used across the tx tests. Returns
// the teardown closure; callers `defer cleanup()`.
func setupTxTest(t *testing.T) (m *Manager, cleanup func()) {
	t.Helper()
	m = newTestManager(t)
	prev := Default()
	SetDefault(m)
	if _, err := m.Exec(context.Background(), `CREATE TABLE users (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT,
        email TEXT,
        age INTEGER,
        created_at DATETIME,
        updated_at DATETIME
    )`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := m.Exec(context.Background(), `CREATE TABLE tx_hook_models (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT,
        created_at DATETIME,
        updated_at DATETIME
    )`); err != nil {
		t.Fatalf("create tx_hook_models: %v", err)
	}
	if _, err := m.Exec(context.Background(), `CREATE TABLE audit_logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        message TEXT,
        created_at DATETIME
    )`); err != nil {
		t.Fatalf("create audit_logs: %v", err)
	}
	hooksFiredOnTx.beforeCreate = false
	hooksFiredOnTx.afterCreate = false
	hooksFiredOnTx.beforeUpdate = false
	hooksFiredOnTx.afterUpdate = false
	return m, func() {
		_ = m.Shutdown(context.Background())
		SetDefault(prev)
	}
}

// TestTxAutoEnroll_HooksFireInsideTx asserts BeforeCreate / AfterCreate
// fire when a Create call inside Transaction auto-enrolls in the tx.
// Hooks must fire on every code path; without this test, a regression
// that silently routes the write through the default Manager would
// still pass.
func TestTxAutoEnroll_HooksFireInsideTx(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()

	err := Default().Transaction(context.Background(), func(ctx context.Context) error {
		_, err := (txHookModel{}).Create(ctx, map[string]any{"name": "x"})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !hooksFiredOnTx.beforeCreate {
		t.Error("BeforeCreate did not fire on auto-enrolled Create")
	}
	if !hooksFiredOnTx.afterCreate {
		t.Error("AfterCreate did not fire on auto-enrolled Create")
	}
}

// TestTxAutoEnroll_UpdateHooksFireInsideTx mirrors the create-hook test
// for the update path. The spec requires hooks to fire inside the tx
// (not the auto-commit fallback) on every code path; without this
// test, a regression that silently routes Update through Default()
// would still pass.
func TestTxAutoEnroll_UpdateHooksFireInsideTx(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()

	// Seed a row inside its own tx so the row exists committed before
	// the hook-firing tx runs.
	var id uint
	if err := Default().Transaction(context.Background(), func(ctx context.Context) error {
		created, err := (txHookModel{}).Create(ctx, map[string]any{"name": "v1"})
		if err != nil {
			return err
		}
		id = created.ID
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Reset Create-side flags so the assertion below targets only
	// the update hooks.
	hooksFiredOnTx.beforeCreate = false
	hooksFiredOnTx.afterCreate = false

	err := Default().Transaction(context.Background(), func(ctx context.Context) error {
		var found txHookModel
		if err := (txHookModel{}).Where("id = ?", id).First(ctx, &found); err != nil {
			return err
		}
		// First() does not mark IsExisting on the destination (a known
		// gap in the read path), so explicitly flag the row as
		// existing to route Save through the update branch where the
		// update hooks are wired.
		markExisting(&found)
		found.Name = "v2"
		return Save(ctx, nil, &found)
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !hooksFiredOnTx.beforeUpdate {
		t.Error("BeforeUpdate did not fire on auto-enrolled Save")
	}
	if !hooksFiredOnTx.afterUpdate {
		t.Error("AfterUpdate did not fire on auto-enrolled Save")
	}
	if hooksFiredOnTx.beforeCreate || hooksFiredOnTx.afterCreate {
		t.Error("create hooks fired on update path")
	}
}

// TestImmutableModel_TxAutoEnroll_RollsBack covers ImmutableModel[T]
// in the auto-enrolled tx path. Append-only audit chains depend on the
// audit row landing in the same tx as the writes it describes.
func TestImmutableModel_TxAutoEnroll_RollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		_, createErr := (auditLog{}).Create(ctx, map[string]any{"message": "x"})
		if createErr != nil {
			return createErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback audit row count = %d, want 0", n)
	}
}

// TestSave_AutoEnrollCommits exercises the package-level Save() helper
// inside a Transaction closure. The caller threads ctx through Save's
// Manager argument: passing the parent Manager + an explicit
// WithTxContext-derived ctx to a chained Query is the ergonomic path
// for the spec example. The simpler form is just to use
// User{}.Save(user), tested below.
func TestSave_AutoEnrollCommits(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	user := &User{Name: "bob", Email: "bob@example.com", Age: 41}
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		return Save(ctx, nil, user)
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if user.ID == 0 {
		t.Error("Save did not stamp ID on commit")
	}
	var name string
	if err := m.DB().QueryRow(`SELECT name FROM users WHERE email = ?`, "bob@example.com").Scan(&name); err != nil {
		t.Fatalf("post-commit row missing: %v", err)
	}
	if name != "bob" {
		t.Errorf("post-commit name = %q, want bob", name)
	}
}

// TestSave_AutoEnrollRollsBack covers the rollback half of the
// Save-in-transaction path.
func TestSave_AutoEnrollRollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	user := &User{Name: "ghost", Email: "ghost@example.com", Age: 1}
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		if saveErr := Save(ctx, nil, user); saveErr != nil {
			return saveErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback count = %d, want 0 (Save escaped tx)", n)
	}
}

// TestQueryUpdateRoutesThroughTx covers Update on a ctx-bound chain.
func TestQueryUpdateRoutesThroughTx(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"before", "u@example.com", 1, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
		_, updErr := (User{}).Where("email = ?", "u@example.com").Update(ctx, map[string]any{"name": "after"})
		if updErr != nil {
			return updErr
		}
		return sentinel
	})

	var name string
	if err := m.DB().QueryRow(`SELECT name FROM users WHERE email = ?`, "u@example.com").Scan(&name); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "before" {
		t.Errorf("post-rollback name = %q, want before (Update escaped the tx)", name)
	}
}

// TestTxDriver_BeginTxDisabled keeps the invariant that nesting a
// transaction inside an existing one is not silently allowed: the
// driver returned by TxFromContext-derived plumbing must reject
// BeginTx. This protects against a "begin a tx inside a tx and
// silently commit it independently" footgun.
func TestTxDriver_BeginTxDisabled(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		// Force a tx-bound driver via WithContext on a chain.
		q := newQuery[User]()
		q.bindTxFromContextValue(ctx)
		drv, ok := q.driver.(*txDriver)
		if !ok {
			t.Fatalf("expected *txDriver after auto-enroll, got %T", q.driver)
		}
		_, beginErr := drv.BeginTx(context.Background(), nil)
		if beginErr == nil {
			t.Error("expected BeginTx to error on tx-bound driver; got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

// TestTxDriver_CloseDisabled keeps the invariant that Close on the
// tx-bound driver wrapper is rejected so we cannot accidentally tear
// down the parent connection pool mid-transaction.
func TestTxDriver_CloseDisabled(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		q := newQuery[User]()
		q.bindTxFromContextValue(ctx)
		drv, ok := q.driver.(*txDriver)
		if !ok {
			t.Fatalf("expected *txDriver after auto-enroll, got %T", q.driver)
		}
		if closeErr := drv.Close(); closeErr == nil {
			t.Error("expected Close on tx-bound driver to error; got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if pingErr := m.Ping(); pingErr != nil {
		t.Errorf("parent Ping after tx-bound Close = %v, want nil", pingErr)
	}
}

// TestTxDriver_DBReturnsNil keeps the invariant that the tx-bound
// driver's DB() returns nil so callers cannot accidentally bypass the
// tx by issuing queries on the parent pool.
func TestTxDriver_DBReturnsNil(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		q := newQuery[User]()
		q.bindTxFromContextValue(ctx)
		drv, ok := q.driver.(*txDriver)
		if !ok {
			t.Fatalf("expected *txDriver after auto-enroll, got %T", q.driver)
		}
		if got := drv.DB(); got != nil {
			t.Errorf("txDriver.DB() = %v, want nil (would silently bypass tx)", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if m.DB() == nil {
		t.Error("parent Manager.DB() = nil after tx clone usage; should be unchanged")
	}
}

// TestQuery_AutoEnrollCreateManyRollsBack verifies the batch-write
// path enrolls in the tx.
func TestQuery_AutoEnrollCreateManyRollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		batch := []User{
			{Name: "a", Email: "a@example.com", Age: 1},
			{Name: "b", Email: "b@example.com", Age: 2},
			{Name: "c", Email: "c@example.com", Age: 3},
		}
		if cmErr := (User{}).CreateMany(ctx, batch); cmErr != nil {
			return cmErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback batch count = %d, want 0 (CreateMany escaped tx)", n)
	}
}
