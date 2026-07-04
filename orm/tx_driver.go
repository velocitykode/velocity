package orm

import (
	"context"
	"database/sql"
	"errors"

	"github.com/velocitykode/velocity/orm/drivers"
)

// txDriver routes data-plane ops (ExecContext / QueryContext /
// QueryRowContext) through a *sql.Tx while delegating dialect concerns
// (Grammar, DriverName, schema introspection) to the wrapped driver.
//
// Created by Manager.WithTx so Save/Update/Delete called inside a
// Transaction closure participate in the caller's transaction instead
// of escaping to the connection pool. Without this, every ORM write
// inside Manager.Transaction would auto-commit through the original
// driver's connection pool, defeating the whole point of the closure.
type txDriver struct {
	drivers.Driver
	tx *sql.Tx
}

// QueryContext routes the read through the bound tx so reads observe
// uncommitted writes from the same transaction.
func (d *txDriver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.tx.QueryContext(ctx, query, drivers.NormalizeTimeArgs(args)...)
}

// QueryRowContext routes the single-row read through the bound tx.
// Save's RETURNING-id path on Postgres lands here.
func (d *txDriver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.tx.QueryRowContext(ctx, query, drivers.NormalizeTimeArgs(args)...)
}

// ExecContext routes mutations through the bound tx so INSERT/UPDATE/
// DELETE participate in the caller's transaction.
func (d *txDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.tx.ExecContext(ctx, query, drivers.NormalizeTimeArgs(args)...)
}

// BeginTx is intentionally disabled on a tx-bound driver. Nesting a
// transaction inside an existing one is a savepoint, not a new tx;
// callers who need that should issue SAVEPOINT statements via
// ExecContext on the underlying *sql.Tx directly.
func (d *txDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return nil, errors.New("velocity/orm: cannot BeginTx on a tx-bound driver; use savepoints on the underlying *sql.Tx")
}

// Close is intentionally disabled. The wrapped driver owns the
// connection pool that other goroutines (and the caller's parent
// Manager) still depend on, so a tx-bound clone must not close it.
// Callers who reach Manager.Shutdown via a derived manager would
// otherwise tear down the parent pool mid-request.
func (d *txDriver) Close() error {
	return errors.New("velocity/orm: cannot Close a tx-bound driver; close the parent Manager instead")
}

// DB returns nil so callers cannot accidentally bypass the bound
// transaction by issuing queries on the parent pool's *sql.DB.
// Reaching Manager.WithTx(tx).DB() and then calling QueryRow on the
// result would silently escape the tx, defeating the whole point of
// the tx-bound manager. Tests that need pool-level access should
// reach for the original Manager, not the WithTx clone.
func (d *txDriver) DB() *sql.DB {
	return nil
}
