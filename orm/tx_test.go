package orm

import (
	"context"
	"database/sql"
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
func (txHookModel) Fillable() []string {
	return []string{"name"}
}

// auditLog covers ImmutableModel[T].WithTx. Append-only chain rows
// must land in the same tx as the writes they describe.
type auditLog struct {
	ImmutableModel[auditLog]
	Message string `orm:"column:message"`
}

func (auditLog) TableName() string  { return "audit_logs" }
func (auditLog) Fillable() []string { return []string{"message"} }

// hooksFiredOnTx records hook invocations for the test below. Local
// package-level mutation is fine because the test is single-threaded
// and resets the flag in setup.
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
// builds the user/hook tables used across the tx tests. Returns the
// teardown closure; callers `defer cleanup()`.
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
	hooksFiredOnTx.beforeCreate = false
	hooksFiredOnTx.afterCreate = false
	hooksFiredOnTx.beforeUpdate = false
	hooksFiredOnTx.afterUpdate = false
	return m, func() {
		_ = m.Shutdown(context.Background())
		SetDefault(prev)
	}
}

func TestModel_WithTx_CreateCommits(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, err := (User{}).WithTx(tx).Create(map[string]any{
			"name":  "alice",
			"email": "alice@example.com",
			"age":   30,
		})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	var name string
	if err := m.DB().QueryRow(`SELECT name FROM users WHERE email = ?`, "alice@example.com").Scan(&name); err != nil {
		t.Fatalf("post-commit row missing: %v", err)
	}
	if name != "alice" {
		t.Errorf("post-commit name = %q, want alice", name)
	}
}

func TestModel_WithTx_CreateRollsBackOnError(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("force rollback")
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, createErr := (User{}).WithTx(tx).Create(map[string]any{
			"name":  "ghost",
			"email": "ghost@example.com",
			"age":   1,
		})
		if createErr != nil {
			return createErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction returned %v, want %v", err, sentinel)
	}

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback row count = %d, want 0 (tx-bound Create escaped the transaction)", n)
	}
}

func TestModel_WithTx_HooksFireInsideTx(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()

	err := Default().Transaction(context.Background(), func(tx *sql.Tx) error {
		_, err := (txHookModel{}).WithTx(tx).Create(map[string]any{"name": "x"})
		return err
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !hooksFiredOnTx.beforeCreate {
		t.Error("BeforeCreate did not fire on tx-bound Create")
	}
	if !hooksFiredOnTx.afterCreate {
		t.Error("AfterCreate did not fire on tx-bound Create")
	}
}

func TestQuery_WithTx_ReadsSeeUncommittedWrites(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, createErr := (User{}).WithTx(tx).Create(map[string]any{
			"name":  "uncommitted",
			"email": "u@example.com",
			"age":   42,
		})
		if createErr != nil {
			return createErr
		}
		// Read inside the tx must see its own write.
		got, err := (User{}).WithTx(tx).Where("email = ?", "u@example.com").Get()
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Name != "uncommitted" {
			t.Errorf("tx-scoped read = %+v, want one row name=uncommitted", got)
		}
		// A read on the pool must NOT see it before commit.
		var n int
		if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "u@example.com").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Errorf("pre-commit pool read count = %d, want 0", n)
		}
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected rollback sentinel")
	}
}

func TestQuery_WithTx_UpdateRoutesThroughTx(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age) VALUES (?, ?, ?)`, "before", "u@example.com", 1); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	_ = m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, updErr := (User{}).WithTx(tx).Where("email = ?", "u@example.com").Update(map[string]any{"name": "after"})
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

func TestTxDriver_BeginTxDisabled(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		txm := m.WithTx(tx)
		_, beginErr := txm.DefaultDriver().BeginTx(context.Background(), nil)
		if beginErr == nil {
			t.Error("expected BeginTx to error on tx-bound driver; got nil")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

func TestTxDriver_CloseDisabled(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		txm := m.WithTx(tx)
		if closeErr := txm.DefaultDriver().Close(); closeErr == nil {
			t.Error("expected Close on tx-bound driver to error; got nil (would tear down parent pool)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	// Parent pool still usable after the closure exits.
	if pingErr := m.Ping(); pingErr != nil {
		t.Errorf("parent Ping after tx-bound Close = %v, want nil", pingErr)
	}
}

func TestImmutableModel_WithTx_CreateRollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	if _, err := m.Exec(context.Background(), `CREATE TABLE audit_logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        message TEXT,
        created_at DATETIME
    )`); err != nil {
		t.Fatalf("create audit_logs: %v", err)
	}

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, createErr := (auditLog{}).WithTx(tx).Create(map[string]any{"message": "x"})
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

func TestQuery_WithTx_CreateManyRollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		batch := []User{
			{Name: "a", Email: "a@example.com", Age: 1},
			{Name: "b", Email: "b@example.com", Age: 2},
			{Name: "c", Email: "c@example.com", Age: 3},
		}
		if cmErr := (User{}).WithTx(tx).CreateMany(batch); cmErr != nil {
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

func TestQuery_WithTx_FirstOrCreateInsidesTx(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"seed", "seed@example.com", 7, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		got, foErr := (User{}).WithTx(tx).FirstOrCreate(
			map[string]any{"email": "seed@example.com"},
			map[string]any{"name": "ignored", "age": 99},
		)
		if foErr != nil {
			return foErr
		}
		if got.Name != "seed" || got.Age != 7 {
			t.Errorf("FirstOrCreate(found) returned %+v, want seed row unchanged", *got)
		}

		created, foErr := (User{}).WithTx(tx).FirstOrCreate(
			map[string]any{"email": "fresh@example.com"},
			map[string]any{"name": "fresh", "age": 21},
		)
		if foErr != nil {
			return foErr
		}
		if created.Name != "fresh" {
			t.Errorf("FirstOrCreate(insert) name = %q, want fresh", created.Name)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel (FirstOrCreate likely errored)", err)
	}

	var count int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "fresh@example.com").Scan(&count); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if count != 0 {
		t.Errorf("post-rollback fresh count = %d, want 0", count)
	}
}

func TestQuery_WithTx_UpdateOrCreateInsidesTx(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"before", "u@example.com", 1, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, uoErr := (User{}).WithTx(tx).UpdateOrCreate(
			map[string]any{"email": "u@example.com"},
			map[string]any{"name": "after", "age": 99},
		)
		return uoErr
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	var name string
	var age int
	if err := m.DB().QueryRow(`SELECT name, age FROM users WHERE email = ?`, "u@example.com").Scan(&name, &age); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "after" || age != 99 {
		t.Errorf("post-commit row = (%q, %d), want (after, 99)", name, age)
	}

	sentinel := errors.New("rollback")
	rerr := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		_, uoErr := (User{}).WithTx(tx).UpdateOrCreate(
			map[string]any{"email": "new@example.com"},
			map[string]any{"name": "n", "age": 5},
		)
		if uoErr != nil {
			return uoErr
		}
		return sentinel
	})
	if !errors.Is(rerr, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", rerr)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "new@example.com").Scan(&n); err != nil {
		t.Fatalf("count new: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback new-row count = %d, want 0", n)
	}
}

func TestTxDriver_DBReturnsNil(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		txm := m.WithTx(tx)
		if got := txm.DB(); got != nil {
			t.Errorf("Manager.WithTx(...).DB() = %v, want nil (would silently bypass tx)", got)
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

func TestQuery_WithTx_NestedRebinds(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	// A second WithTx call on a chain that is already tx-bound must
	// rebind against the original pool driver, not stack another
	// txDriver wrapper. Realistic case: a helper that defensively
	// re-applies WithTx, or middleware that doesn't know whether the
	// query handed in is already bound.
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		q := (User{}).WithTx(tx).WithTx(tx)
		td, ok := q.driver.(*txDriver)
		if !ok {
			t.Fatalf("expected *txDriver after nested WithTx, got %T", q.driver)
		}
		if _, stacked := td.Driver.(*txDriver); stacked {
			t.Error("nested WithTx stacked txDriver wrappers; expected rebind to pool driver")
		}
		_, cErr := q.Create(map[string]any{
			"name":  "rebound",
			"email": "rebound@example.com",
			"age":   1,
		})
		return cErr
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "rebound@example.com").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rebound row count = %d, want 1", n)
	}
}

// TestSave_WithTxManagerCommits exercises the spec-style call site
// orm.Save(m.WithTx(tx), &u): the public Save resolves the tx-bound
// driver from the Manager handle rather than from a Query chain.
func TestSave_WithTxManagerCommits(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	user := &User{Name: "bob", Email: "bob@example.com", Age: 41}
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		return Save(m.WithTx(tx), user)
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

// TestSave_WithTxManagerRollsBack covers the rollback half of the
// orm.Save(m.WithTx(tx), &u) path.
func TestSave_WithTxManagerRollsBack(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	user := &User{Name: "ghost", Email: "ghost@example.com", Age: 1}
	err := m.Transaction(context.Background(), func(tx *sql.Tx) error {
		if saveErr := Save(m.WithTx(tx), user); saveErr != nil {
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
		t.Errorf("post-rollback count = %d, want 0 (Save via tx-bound Manager escaped)", n)
	}
}

// TestModel_WithTx_UpdateHooksFireInsideTx mirrors the create-hook test
// for the update path. The spec requires hooks to fire inside the tx
// (not the auto-commit fallback) on every code path; without this
// test, a regression that silently routes Update through Default()
// would still pass.
func TestModel_WithTx_UpdateHooksFireInsideTx(t *testing.T) {
	_, cleanup := setupTxTest(t)
	defer cleanup()

	// Seed a row inside its own tx so the row exists committed before
	// the hook-firing tx runs.
	var id uint
	if err := Default().Transaction(context.Background(), func(tx *sql.Tx) error {
		created, err := (txHookModel{}).WithTx(tx).Create(map[string]any{"name": "v1"})
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

	err := Default().Transaction(context.Background(), func(tx *sql.Tx) error {
		var found txHookModel
		if err := (txHookModel{}).WithTx(tx).Where("id = ?", id).First(&found); err != nil {
			return err
		}
		// First() does not mark IsExisting on the destination (a known
		// gap in the read path), so explicitly flag the row as
		// existing to route Save through the update branch where the
		// update hooks are wired.
		markExisting(&found)
		found.Name = "v2"
		return (txHookModel{}).WithTx(tx).Save(&found)
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
	if !hooksFiredOnTx.beforeUpdate {
		t.Error("BeforeUpdate did not fire on tx-bound Save")
	}
	if !hooksFiredOnTx.afterUpdate {
		t.Error("AfterUpdate did not fire on tx-bound Save")
	}
	if hooksFiredOnTx.beforeCreate || hooksFiredOnTx.afterCreate {
		t.Error("create hooks fired on update path")
	}
}

func TestManager_WithTx_NilTxReturnsReceiver(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	if got := m.WithTx(nil); got != m {
		t.Errorf("WithTx(nil) = %p, want receiver %p", got, m)
	}
}
