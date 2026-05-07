package orm

import (
	"context"
	"database/sql"
	"errors"
	"testing"
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

// hooksFiredOnTx records hook invocations for the test below. Local
// package-level mutation is fine because the test is single-threaded
// and resets the flag in setup.
var hooksFiredOnTx struct {
	beforeCreate bool
	afterCreate  bool
}

func (m *txHookModel) BeforeCreate() error {
	hooksFiredOnTx.beforeCreate = true
	return nil
}

func (m *txHookModel) AfterCreate() error {
	hooksFiredOnTx.afterCreate = true
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

func TestManager_WithTx_NilTxReturnsReceiver(t *testing.T) {
	m, cleanup := setupTxTest(t)
	defer cleanup()

	if got := m.WithTx(nil); got != m {
		t.Errorf("WithTx(nil) = %p, want receiver %p", got, m)
	}
}
