package orm

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRawQuery_Exec_AutoEnrollsInTransaction pins the contract that a
// raw SQL write executed inside Manager.Transaction with the closure
// ctx auto-enrolls in the surrounding tx and rolls back when the
// closure returns an error. Before RawQuery terminals took ctx as the
// first positional argument they had no way to observe the tx slot, so
// raw writes silently auto-committed against the pool driver, exactly
// the footgun the ctx-first reframe was meant to kill.
func TestRawQuery_Exec_AutoEnrollsInTransaction(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	// Seed one row so UPDATE has something to touch.
	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"alice", "alice@example.com", 30, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sentinel := errors.New("rollback")
	err := m.Transaction(context.Background(), func(ctx context.Context) error {
		rq := NewRawQuery[User](`UPDATE users SET age = ? WHERE name = ?`, 99, "alice")
		rq.driver = m.DefaultDriver()
		result, execErr := rq.Exec(ctx)
		if execErr != nil {
			return execErr
		}
		affected, raErr := result.RowsAffected()
		if raErr != nil {
			t.Fatalf("RowsAffected: %v", raErr)
		}
		if affected != 1 {
			t.Fatalf("Exec affected = %d, want 1", affected)
		}

		// Inside the closure the row should reflect the update via the
		// same tx-bound driver. We use a fresh RawQuery scoped to the
		// same ctx so the Scan terminal observes the tx slot too.
		rq2 := NewRawQuery[User](`SELECT age FROM users WHERE name = ?`, "alice")
		rq2.driver = m.DefaultDriver()
		var age int
		if scanErr := rq2.Scan(ctx, &age); scanErr != nil {
			t.Fatalf("in-tx Scan: %v", scanErr)
		}
		if age != 99 {
			t.Fatalf("in-tx age = %d, want 99 (raw update did not enroll in tx)", age)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Transaction returned %v, want sentinel", err)
	}

	// After rollback the original value must remain because the raw
	// UPDATE participated in the rolled-back tx.
	var age int
	if err := m.DB().QueryRow(`SELECT age FROM users WHERE name = ?`, "alice").Scan(&age); err != nil {
		t.Fatalf("post-rollback select: %v", err)
	}
	if age != 30 {
		t.Errorf("post-rollback age = %d, want 30 (raw Exec escaped tx and auto-committed)", age)
	}
}

// TestRawQuery_Exec_OptOutWithPlainCtx pins the documented opt-out
// path on RawQuery: a ctx that does not carry the tx slot (typically
// the caller's original ctx captured before Manager.Transaction) must
// route through the pool driver and escape the surrounding tx. This is
// the escape hatch for fire-and-forget audit / metrics writes inside an
// otherwise-rolling-back tx. The sqlite :memory: pool makes a cross
// connection write assertion brittle, so we verify the rebind invariant
// directly (mirroring TestWrite_OptOutPlainCtxBindsPoolDriver).
func TestRawQuery_Exec_OptOutWithPlainCtx(t *testing.T) {
	m, cleanup := setupTxContextTest(t)
	defer cleanup()

	if _, err := m.Exec(context.Background(),
		`INSERT INTO users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"bob", "bob@example.com", 40, time.Now(), time.Now(),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rootCtx := context.Background()
	err := m.Transaction(rootCtx, func(txCtx context.Context) error {
		// Sanity: closure ctx carries the tx slot.
		if _, ok := TxFromContext(txCtx); !ok {
			t.Fatal("closure ctx missing tx slot")
		}

		// Step 1: with the closure ctx, the RawQuery binds the tx
		// driver, the auto-enroll path.
		rq := NewRawQuery[User](`UPDATE users SET age = ? WHERE name = ?`, 1, "bob")
		rq.driver = m.DefaultDriver()
		rq.bindTxFromContextValue(txCtx)
		if _, ok := rq.driver.(*txDriver); !ok {
			t.Errorf("closure-ctx rq driver = %T, want *txDriver", rq.driver)
		}

		// Step 2: with rootCtx (no tx slot), the same rq must rebind
		// down to the pool driver. This is the explicit opt-out
		// contract: pass the original pre-tx ctx and the call escapes.
		rq.bindTxFromContextValue(rootCtx)
		if _, ok := rq.driver.(*txDriver); ok {
			t.Errorf("plain-ctx rebind kept tx wrapper on RawQuery; expected pool driver")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}
}

// TestRawQuery_TerminalsRequireCtx is a compile-time pin: the four
// terminal methods on RawQuery[T] all take ctx as the first positional
// argument. If anyone refactors the signatures back to ctx-free this
// closure will stop compiling, surfacing the regression at build time.
func TestRawQuery_TerminalsRequireCtx(t *testing.T) {
	t.Helper()
	_ = func(ctx context.Context, rq *RawQuery[User]) {
		var u User
		_ = rq.First(ctx, &u)
		_, _ = rq.Get(ctx)
		_, _ = rq.Exec(ctx)
		var n int
		_ = rq.Scan(ctx, &n)
	}
}
