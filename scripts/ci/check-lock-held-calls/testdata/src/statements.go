package lockheld

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"sync"
	"time"

	"example.com/lockheld/internal/ownctx"
	"example.com/lockheld/orm/drivers"
)

// Statement calls: an instrumented pool runs its statement observer and
// query logger inside every statement, so a statement under a lock is
// user code unless its context holds the observation (ownctx.Hold).

type StmtStore struct {
	mu    sync.Mutex
	db    *sql.DB
	obs   drivers.StatementObserver
	other drivers.Other
	res   driver.Result
	sres  sql.Result
}

func (s *StmtStore) Held(ctx context.Context) error {
	owned, held := ownctx.Hold(ctx)
	defer held.Release()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.db.ExecContext(owned, "x"); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(owned, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row := tx.QueryRowContext(owned, "x")
	var n int
	if err := row.Scan(&n); err != nil {
		return err
	}
	if err := s.db.QueryRowContext(owned, "x").Scan(&n); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(owned, "x")
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	_ = rows.Close()
	_, _ = s.sres.RowsAffected() // database/sql's own result: not the driver's interface
	return tx.Commit()
}

func (s *StmtStore) HeldDetached(ctx context.Context) {
	owned, cancel, held := ownctx.HoldDetached(ctx, time.Second)
	defer cancel()
	defer held.Release()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(owned, "x")
}

func (s *StmtStore) NotHeld(ctx context.Context) {
	owned := ownctx.Bridge(ctx)
	held, _ := ownctx.Hold(ctx) // want hold
	bounded, cancel := context.WithTimeout(held, time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(owned, "x")                // want statement
	_, _ = s.db.ExecContext(context.Background(), "x") // want statement
	_, _ = s.db.ExecContext(bounded, "x")              // want statement
	_, _ = s.db.Exec("x")                              // want statement
	row := s.db.QueryRowContext(owned, "x")            // want statement
	var n int
	_ = row.Scan(&n) // want statement
	rows, _ := s.db.QueryContext(held, "x")
	_ = rows.Close()
	_, _ = s.db.PrepareContext(owned, "x") // want statement
}

func (s *StmtStore) DriverAndObserver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.res.RowsAffected() // want statement
	_ = s.obs.Observing()       // want statement
	s.obs.ObserveStatement("x") // want statement
	_ = s.other.Name()          // another interface of the package: listed by -all only
}

// Unlocked: nothing is held.
func (s *StmtStore) Unlocked() {
	_, _ = s.db.Exec("x")
	_ = s.obs.Observing()
}

// execHeld's ctx is closed: every call site passes a holding context.
func (s *StmtStore) execHeld(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "x")
}

func (s *StmtStore) ClosedParam(ctx context.Context) {
	owned, held := ownctx.Hold(ctx)
	defer held.Release()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execHeld(owned)
	s.execHeld(owned)
}

// execOpen's ctx is open: one call site passes a context that does not
// hold.
func (s *StmtStore) execOpen(ctx context.Context) {
	_, _ = s.db.ExecContext(ctx, "x")
}

func (s *StmtStore) OpenParam(ctx context.Context) {
	owned, held := ownctx.Hold(ctx)
	defer held.Release()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execOpen(owned)                // want reach
	s.execOpen(context.Background()) // want reach
}

// pingOwned's ctx is closed and every call site passes an owned context,
// so the ctx rule treats it as owned.
func (s *StmtStore) pingOwned(ctx context.Context) {
	_ = s.db.PingContext(ctx)
}

func (s *StmtStore) OwnedParam(ctx context.Context) {
	owned := ownctx.Bridge(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingOwned(owned)
}

// Hold pairing: a Held must be released by a defer in the function that
// holds it, so a panic or early return still delivers its reports.
func (s *StmtStore) HoldExplicitRelease(ctx context.Context) {
	owned, held := ownctx.Hold(ctx) // want hold
	s.mu.Lock()
	_, _ = s.db.ExecContext(owned, "x")
	s.mu.Unlock()
	held.Release()
}

func (s *StmtStore) HoldDiscarded(ctx context.Context) {
	owned, _ := ownctx.Hold(ctx) // want hold
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(owned, "x")
}

func (s *StmtStore) HoldReleasedInLiteral(ctx context.Context) {
	owned, held := ownctx.Hold(ctx) // want hold
	func() { defer held.Release() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(owned, "x")
}

func (s *StmtStore) HoldDetachedDeferred(ctx context.Context) {
	var owned, cancel, held = ownctx.HoldDetached(ctx, time.Second)
	defer cancel()
	defer held.Release()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.db.ExecContext(owned, "x")
}
