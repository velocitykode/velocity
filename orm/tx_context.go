package orm

import (
	"context"
	"database/sql"
)

// txCtxKey is the unexported key used to attach a *sql.Tx to a
// context.Context. Keeping it unexported prevents callers from
// constructing a value that mimics the orm's tx slot, which preserves
// the invariant that the tx in ctx was put there by a Manager method
// (Transaction or WithTxContext) holding a real *sql.Tx.
type txCtxKey struct{}

// WithTxContext returns a derived context that carries tx. Subsequent
// ORM terminals (Save, Create, Update, Delete, FirstOrCreate,
// UpdateOrCreate, CreateMany, Increment, Get, First, Count, Pluck, ...)
// receiving this context as their first positional argument will
// automatically participate in tx.
//
// Manager.Transaction wires this slot for the caller; direct use of
// WithTxContext is reserved for advanced flows that begin a tx via
// Manager.Begin and want to thread it through ctx without using the
// closure-style helper. Passing nil tx is a no-op (returns ctx).
//
// Security: the slot is keyed by an unexported type so external code
// cannot smuggle a forged *sql.Tx into the orm via a hand-crafted
// context. The tx value can only be set by callers inside the orm
// package or callers who already hold a valid *sql.Tx returned by
// Manager.Begin / driver.BeginTx.
func WithTxContext(ctx context.Context, tx *sql.Tx) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// TxFromContext returns the *sql.Tx attached to ctx by WithTxContext
// or Manager.Transaction, plus a bool indicating whether one was
// found. Returns (nil, false) when ctx is nil or carries no tx.
//
// ORM internals call this to upgrade a pool-bound Query to a tx-bound
// Query at terminal time. Application code rarely needs to call this
// directly; reach for it only when integrating non-ORM SQL helpers
// that should join the same transaction (e.g. raw migrations,
// driver-specific SAVEPOINT issuance).
func TxFromContext(ctx context.Context) (*sql.Tx, bool) {
	if ctx == nil {
		return nil, false
	}
	tx, ok := ctx.Value(txCtxKey{}).(*sql.Tx)
	if !ok || tx == nil {
		return nil, false
	}
	return tx, true
}
