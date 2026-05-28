package orm

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// setupTxContextTest mirrors setupTxTest but is local to this file so
// tx_test.go can be inspected and changed independently. It seeds the
// users / audit_logs tables that the context-aware transaction tests
// below rely on.
func setupTxContextTest(t *testing.T) (*Manager, func()) {
	t.Helper()
	m := newTestManager(t)
	// sqlite `:memory:` gives each pool connection its own private
	// database. Tx-cancellation tests grab a fresh connection for the
	// post-rollback COUNT verification and intermittently land on one
	// that never saw the CREATE TABLE, surfacing "no such table". Pin
	// to a single connection so every query in the test sees the same
	// in-memory state.
	m.DB().SetMaxOpenConns(1)
	prev := Default()
	SetDefault(m)
	mustExec := func(sqlStmt string) {
		if _, err := m.Exec(context.Background(), sqlStmt); err != nil {
			t.Fatalf("exec %q: %v", sqlStmt, err)
		}
	}
	mustExec(`CREATE TABLE users (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT,
        email TEXT,
        age INTEGER,
        created_at DATETIME,
        updated_at DATETIME
    )`)
	mustExec(`CREATE TABLE audit_logs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        message TEXT,
        created_at DATETIME
    )`)
	return m, func() {
		_ = m.Shutdown(context.Background())
		SetDefault(prev)
	}
}

// TestTransaction_AutoEnrollsCreate is the headline regression: a
// Create call inside Manager.Transaction with no per-call WithTx
// still participates in the tx and rolls back when the closure
// returns an error. Before this change the only way to get this
// behaviour was to remember to call WithTx(tx) on every helper,
// which silently auto-committed when forgotten.
func TestTransaction_AutoEnrollsCreate(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		_, createErr := (User{}).Create(ctx, map[string]any{
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
		t.Fatalf("Transaction returned %v, want sentinel (Create likely escaped tx)", err)
	}

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback row count = %d, want 0 (Create auto-enroll regression)", n)
	}
}

// TestTransaction_CommitPersists is the success-path counterpart:
// when fn returns nil the tx commits and the rows survive.
func TestTransaction_CommitPersists(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		_, createErr := (User{}).Create(ctx, map[string]any{
			"name":  "alice",
			"email": "alice@example.com",
			"age":   30,
		})
		return createErr
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

// TestTransaction_AutoEnrollsUpdate covers the update path: the
// canonical "edit a row that an earlier statement found" pattern
// must roll back wholesale on error.
func TestTransaction_AutoEnrollsUpdate(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
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
		t.Errorf("post-rollback name = %q, want before (Update escaped the tx via ctx-bound chain)", name)
	}
}

// TestTransaction_AutoEnrollsForceDelete covers ForceDelete which
// does not pass through applySoftDeleteScope, so it depends on the
// explicit bindTxFromContext call added to ForceDelete.
func TestTransaction_AutoEnrollsForceDelete(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"victim", "v@example.com", 1, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
		_, delErr := (User{}).Where("email = ?", "v@example.com").ForceDelete(ctx)
		if delErr != nil {
			return delErr
		}
		return sentinel
	})

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "v@example.com").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("post-rollback victim count = %d, want 1 (ForceDelete escaped tx)", n)
	}
}

// TestTransaction_AutoEnrollsCreateMany covers the batch-write
// path. The whole batch must vanish on rollback.
func TestTransaction_AutoEnrollsCreateMany(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
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

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback batch count = %d, want 0 (CreateMany escaped tx)", n)
	}
}

// TestTransaction_AutoEnrollsFirstOrCreate covers the canonical
// idempotency entry point. The "insert" branch must roll back; the
// "found" branch must observe rows visible at tx start.
func TestTransaction_AutoEnrollsFirstOrCreate(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"seed", "seed@example.com", 7, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		got, foErr := (User{}).FirstOrCreate(ctx,
			map[string]any{"email": "seed@example.com"},
			map[string]any{"name": "ignored", "age": 99},
		)
		if foErr != nil {
			return foErr
		}
		if got.Name != "seed" || got.Age != 7 {
			t.Errorf("FirstOrCreate(found) returned %+v, want seed row unchanged", *got)
		}
		_, foErr = (User{}).FirstOrCreate(ctx,
			map[string]any{"email": "fresh@example.com"},
			map[string]any{"name": "fresh", "age": 21},
		)
		if foErr != nil {
			return foErr
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction = %v, want sentinel", err)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users WHERE email = ?`, "fresh@example.com").Scan(&n); err != nil {
		t.Fatalf("count fresh: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback fresh count = %d, want 0 (FirstOrCreate-insert escaped tx)", n)
	}
}

// TestTransaction_AutoEnrollsUpdateOrCreate covers the
// "idempotent write" entry point. UpdateOrCreate must route through
// the bound tx for both the lookup and the write.
func TestTransaction_AutoEnrollsUpdateOrCreate(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	now := time.Now()
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"before", "u@example.com", 1, now, now); err != nil {
		t.Fatalf("seed: %v", err)
	}

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		_, uoErr := (User{}).UpdateOrCreate(ctx,
			map[string]any{"email": "u@example.com"},
			map[string]any{"name": "after", "age": 99},
		)
		return uoErr
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	var name string
	if err := m.DB().QueryRow(`SELECT name FROM users WHERE email = ?`, "u@example.com").Scan(&name); err != nil {
		t.Fatalf("read: %v", err)
	}
	if name != "after" {
		t.Errorf("post-commit name = %q, want after", name)
	}
}

// TestTransaction_RollbackOnPanic asserts panics inside the closure
// roll back the transaction and re-panic.
func TestTransaction_RollbackOnPanic(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic to propagate, got nil")
		}
		var n int
		if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 0 {
			t.Errorf("post-panic row count = %d, want 0 (panic must rollback)", n)
		}
	}()

	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
		_, _ = (User{}).Create(ctx, map[string]any{
			"name":  "panicked",
			"email": "p@example.com",
			"age":   1,
		})
		panic("boom")
	})
}

// TestTransaction_MixedWritesAllRollBack is the bug this whole change
// exists to prevent. Any combination of state-changing helpers inside
// the closure must enroll in the same tx; partial commits (some
// writes survive a rollback) must be impossible without an explicit
// opt-out (see TestTransaction_ExplicitOptOutEscape).
func TestTransaction_MixedWritesAllRollBack(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	sentinel := errors.New("rollback")
	_ = m.Transaction(context.Background(), func(ctx context.Context) error {
		// Insert via Create.
		if _, err := (User{}).Create(ctx, map[string]any{
			"name": "one", "email": "one@example.com", "age": 1,
		}); err != nil {
			return err
		}
		// Insert via Save on a model pointer.
		two := &User{Name: "two", Email: "two@example.com", Age: 2}
		if err := Save(ctx, nil, two); err != nil {
			return err
		}
		// Insert via CreateMany.
		batch := []User{{Name: "three", Email: "three@example.com", Age: 3}}
		if err := (User{}).CreateMany(ctx, batch); err != nil {
			return err
		}
		// Insert via FirstOrCreate.
		if _, err := (User{}).FirstOrCreate(ctx,
			map[string]any{"email": "four@example.com"},
			map[string]any{"name": "four", "age": 4},
		); err != nil {
			return err
		}
		// Insert via UpdateOrCreate.
		if _, err := (User{}).UpdateOrCreate(ctx,
			map[string]any{"email": "five@example.com"},
			map[string]any{"name": "five", "age": 5},
		); err != nil {
			return err
		}
		// And one immutable model insert via the audit_logs table.
		if _, err := (auditLog{}).Create(ctx, map[string]any{"message": "x"}); err != nil {
			return err
		}
		return sentinel
	})

	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback user count = %d, want 0 (one of the write helpers escaped tx)", n)
	}
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM audit_logs`).Scan(&n); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if n != 0 {
		t.Errorf("post-rollback audit count = %d, want 0 (auditLog Create escaped tx)", n)
	}
}

// TestTransaction_ExplicitOptOutBindsPoolDriver documents the only
// supported way to opt a write OUT of the auto-enrolled tx: pass a
// context that does not carry the tx (typically the parent ctx
// captured before Manager.Transaction). Use cases include emitting
// an audit row to a separate audit DB connection, or a
// fire-and-forget log write that must survive a business-logic
// rollback.
//
// We check the binding (q.driver type) rather than the row count:
// SQLite ":memory:" databases are per-connection, so a pool-routed
// write inside a tx scope would land on a different DB than the one
// the test reads back from. The structural assertion is the
// invariant: a query bound to a ctx without the tx slot uses the
// pool driver, not the tx-bound driver.
func TestTransaction_ExplicitOptOutBindsPoolDriver(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	rootCtx := context.Background()
	err := m.Transaction(rootCtx, func(ctx context.Context) error {
		// In-tx terminal: bindTxFromContextValue must wrap the pool
		// driver in a *txDriver bound to the closure's tx.
		txQ := newQuery[User]()
		txQ.bindTxFromContextValue(ctx)
		if _, ok := txQ.driver.(*txDriver); !ok {
			t.Errorf("in-tx chain driver = %T, want *txDriver", txQ.driver)
		}
		// Opt-out terminal: rootCtx has no tx slot, must use pool driver.
		poolQ := newQuery[User]()
		poolQ.bindTxFromContextValue(rootCtx)
		if _, ok := poolQ.driver.(*txDriver); ok {
			t.Errorf("opt-out chain driver = *txDriver, want pool driver (rootCtx has no tx slot)")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

// TestTransaction_ReadsSeeUncommittedWrites mirrors the existing
// behaviour: a read inside the closure must observe the tx's own
// uncommitted writes; a read on the pool must NOT see them until
// commit.
func TestTransaction_ReadsSeeUncommittedWrites(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		if _, err := (User{}).Create(ctx, map[string]any{
			"name":  "uncommitted",
			"email": "u@example.com",
			"age":   42,
		}); err != nil {
			return err
		}
		got, err := (User{}).Where("email = ?", "u@example.com").Get(ctx)
		if err != nil {
			return err
		}
		if len(got) != 1 || got[0].Name != "uncommitted" {
			t.Errorf("tx-scoped read = %+v, want one row name=uncommitted", got)
		}
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

// TestWithTxContext_RoundTrip is the unit test for the helper pair:
// WithTxContext(ctx, tx) must produce a ctx that TxFromContext
// returns tx for, and a nil-tx call must be a no-op.
func TestWithTxContext_RoundTrip(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	tx, err := m.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback()

	root := context.Background()
	bound := WithTxContext(root, tx)

	got, ok := TxFromContext(bound)
	if !ok || got != tx {
		t.Errorf("TxFromContext(bound) = (%p, %v), want (%p, true)", got, ok, tx)
	}
	if _, ok := TxFromContext(root); ok {
		t.Error("TxFromContext(root) returned ok=true, want false (root must not carry tx)")
	}

	// Nil-tx is a no-op: returns ctx unchanged.
	if got := WithTxContext(root, nil); got != root {
		t.Errorf("WithTxContext(root, nil) = %v, want root unchanged", got)
	}

	// Nil ctx is normalised to Background.
	//lint:ignore SA1012 testing the nil-input contract of the helper
	if got := WithTxContext(nil, nil); got == nil {
		t.Error("WithTxContext(nil, nil) returned nil; want non-nil ctx")
	}
}

// TestTransaction_NilFnIsNoOp asserts the helper handles a nil
// callback without panicking and without starting a tx.
func TestTransaction_NilFnIsNoOp(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	if err := m.Transaction(context.Background(), nil); err != nil {
		t.Errorf("Transaction(_, nil) = %v, want nil", err)
	}
}

// TestTransaction_BindIdempotent asserts that re-binding through the
// same ctx is idempotent: calling bindTxFromContextValue twice must
// not stack txDriver wrappers. This guards the swap-not-stack
// invariant that prevents Driver methods from being double-shadowed.
func TestTransaction_BindIdempotent(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		q := newQuery[User]()
		q.bindTxFromContextValue(ctx)
		q.bindTxFromContextValue(ctx)
		drv, ok := q.driver.(*txDriver)
		if !ok {
			t.Fatalf("expected *txDriver after bind, got %T", q.driver)
		}
		if _, nested := drv.Driver.(*txDriver); nested {
			t.Errorf("bindTxFromContextValue stacked txDriver wrappers; expected single wrap")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

// TestTxFromContext_Nil safely returns (nil, false).
func TestTxFromContext_Nil(t *testing.T) {
	//lint:ignore SA1012 testing the nil-input contract of the helper
	tx, ok := TxFromContext(nil)
	if ok || tx != nil {
		t.Errorf("TxFromContext(nil) = (%v, %v), want (nil, false)", tx, ok)
	}
}

// TestTxFromContext_NoTx returns (nil, false) for a ctx without a tx
// slot.
func TestTxFromContext_NoTx(t *testing.T) {
	tx, ok := TxFromContext(context.Background())
	if ok || tx != nil {
		t.Errorf("TxFromContext(bg) = (%v, %v), want (nil, false)", tx, ok)
	}
}

// TestTransaction_NestedInnerTxIsolatedFromOuterCtx documents the
// nested-Transaction contract: the inner closure receives a ctx whose
// tx slot is the inner *sql.Tx, not the outer one. Inner ORM calls
// chain off the inner ctx and so participate in the inner tx.
//
// This is the savepoint-style boundary the framework offers: each
// Transaction call gets its own *sql.Tx (the underlying driver is
// responsible for issuing real SAVEPOINTs if the pool is shared and
// the dialect supports them; on per-connection pools like SQLite
// memory each tx lives on its own connection). Either way, the
// assertion the framework guarantees is that inner ORM calls bind to
// the INNER tx rather than escaping back to the outer one.
func TestTransaction_NestedInnerTxIsolatedFromOuterCtx(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	var outerTx, innerTx *sql.Tx
	err := m.Transaction(context.Background(), func(outerCtx context.Context) error {
		var ok bool
		outerTx, ok = TxFromContext(outerCtx)
		if !ok || outerTx == nil {
			t.Fatal("outer ctx missing tx slot")
		}
		return m.Transaction(outerCtx, func(innerCtx context.Context) error {
			innerTx, ok = TxFromContext(innerCtx)
			if !ok || innerTx == nil {
				t.Fatal("inner ctx missing tx slot")
			}
			if innerTx == outerTx {
				t.Error("inner tx == outer tx; expected a fresh *sql.Tx for the nested scope")
			}
			// A terminal rooted at innerCtx must bind to innerTx, not
			// outerTx, which is the no-bypass invariant the rest of
			// the suite relies on.
			q := newQuery[User]()
			q.bindTxFromContextValue(innerCtx)
			drv, ok := q.driver.(*txDriver)
			if !ok {
				t.Fatalf("inner chain driver = %T, want *txDriver", q.driver)
			}
			if drv.tx != innerTx {
				t.Error("inner chain bound to outer tx; nested ctx propagation regressed")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("nested Transaction: %v", err)
	}
}

// TestTransaction_CtxCancellationRollsBack asserts that when the
// caller's ctx is cancelled mid-tx, the partial work does not commit.
// The closure observes the cancellation via ctx.Err() and returns it,
// so the manager rolls back through the standard error path.
//
// Even if the closure misses the cancellation and returns nil, the
// stdlib *sql.Tx is bound to the BeginTx ctx and the deferred
// background goroutine in database/sql calls Rollback on cancel, so
// Commit returns a sql.ErrTxDone-like error rather than persisting the
// row. Either way, no row survives, which is the invariant we test.
func TestTransaction_CtxCancellationRollsBack(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	err := m.Transaction(ctx, func(txCtx context.Context) error {
		if _, err := (User{}).Create(txCtx, map[string]any{
			"name":  "doomed",
			"email": "doomed@example.com",
			"age":   1,
		}); err != nil {
			return err
		}
		// Cancel mid-transaction: subsequent calls on the chain see
		// ctx.Err(), and the manager's Commit path observes the
		// cancellation when fn returns.
		cancel()
		return txCtx.Err()
	})
	if err == nil {
		t.Fatal("Transaction returned nil after ctx cancellation; expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Transaction err = %v, want errors.Is(_, context.Canceled)", err)
	}
	var n int
	if err := m.DB().QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("post-cancel row count = %d, want 0 (cancellation must roll back)", n)
	}
}

// TestTransaction_CtxAlreadyCancelledReturnsErr asserts that calling
// Transaction with an already-cancelled ctx fails fast: BeginTx
// returns the ctx error, fn never runs, and no rollback path is
// invoked.
func TestTransaction_CtxAlreadyCancelledReturnsErr(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	err := m.Transaction(ctx, func(_ context.Context) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("Transaction returned nil for cancelled ctx; expected error")
	}
	if called {
		t.Error("fn ran despite cancelled ctx; BeginTx should have failed before invoking fn")
	}
}

// TestWriteEntryPoints_RequireCtxArg is the documentation-style guard
// that every state-changing entry point on Query[T] takes ctx as its
// first positional argument. This is enforced by the compiler: each
// call below would be a "not enough arguments" / "cannot use ... as
// context.Context value" error if a future refactor dropped ctx from
// any signature. The test keeps the canonical shape close to the
// regression suite so a missed signature shows up here first.
//
// The test does not fire any SQL (it builds chains and abandons them);
// the goal is to pin the type signature, not to exercise the driver.
func TestWriteEntryPoints_RequireCtxArg(t *testing.T) {
	_, cleanup := setupTxContextTest(t)
	defer cleanup()

	ctx := context.Background()

	// Every helper below is the canonical write entry point; each
	// must accept ctx as the first positional argument.
	_ = func() (*User, error) {
		return (User{}).Create(ctx, map[string]any{})
	}
	_ = func() error {
		return Save(ctx, nil, &User{})
	}
	_ = func() error {
		return (User{}).CreateMany(ctx, nil)
	}
	_ = func() (*User, error) {
		return (User{}).FirstOrCreate(ctx, nil, nil)
	}
	_ = func() (*User, error) {
		return (User{}).UpdateOrCreate(ctx, nil, nil)
	}
	_ = func() (int64, error) {
		return (User{}).Update(ctx, map[string]any{}, map[string]any{})
	}
	_ = func() (int64, error) {
		return (User{}).Where("id > ?", 0).Delete(ctx)
	}
	_ = func() (int64, error) {
		return (User{}).Where("id > ?", 0).ForceDelete(ctx)
	}
	_ = func() error {
		return (User{}).Increment(ctx, "age")
	}
	_ = func() error {
		return (User{}).Decrement(ctx, "age")
	}
	// Package-level helpers also take ctx first.
	_ = func() error {
		return Save(ctx, nil, &User{})
	}
	_ = func() error {
		return CreateMany(ctx, []User{})
	}
}

// TestWrite_OptOutPlainCtxBindsPoolDriver pins the documented opt-out
// path: a write that receives a ctx without the tx slot routes
// through the pool driver, NOT the surrounding tx. Without this the
// "every write enrolls automatically" guarantee would silently turn
// into "every write enrolls and there's no escape", breaking
// fire-and-forget audit / metrics writes that must survive a
// business-logic rollback.
func TestWrite_OptOutPlainCtxBindsPoolDriver(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	rootCtx := context.Background()
	err := m.Transaction(rootCtx, func(txCtx context.Context) error {
		// Sanity: the closure ctx carries the tx.
		if _, ok := TxFromContext(txCtx); !ok {
			t.Fatal("closure ctx missing tx slot")
		}
		// A query rooted at txCtx must be tx-bound.
		txQ := newQuery[User]()
		txQ.bindTxFromContextValue(txCtx)
		// Calling a write with the closure ctx keeps the tx binding.
		// We assert the pre-write driver is tx-bound here; the actual
		// SQL is not exercised because :memory: cross-connection
		// semantics make COUNT-based assertions unreliable for the
		// pool-driver branch in a single test.
		if _, ok := txQ.driver.(*txDriver); !ok {
			t.Errorf("closure-ctx chain driver = %T, want *txDriver", txQ.driver)
		}
		// Calling a write with a plain ctx must unwrap to the pool
		// driver. We verify by inspecting the driver after the helper
		// has performed its bind step (an internal invariant: the
		// rebind is in-place on q.driver).
		txQ.bindTxFromContextValue(rootCtx)
		if _, ok := txQ.driver.(*txDriver); ok {
			t.Errorf("plain-ctx rebind kept tx wrapper; expected pool driver")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}
