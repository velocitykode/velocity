package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/orm/drivers"
)

// BulkOp identifies the kind of bulk write that triggered a
// BulkAfterCommit callback.
type BulkOp string

const (
	// BulkOpUpdate is dispatched for Query.Update.
	BulkOpUpdate BulkOp = "update"
	// BulkOpDelete is dispatched for Query.Delete on a model that
	// supports soft deletes (the underlying statement is an UPDATE
	// stamping deleted_at).
	BulkOpDelete BulkOp = "delete"
	// BulkOpForceDelete is dispatched for Query.ForceDelete and for
	// Query.Delete on models without soft-delete support (the
	// underlying statement is a DELETE).
	BulkOpForceDelete BulkOp = "force_delete"
)

// BulkAfterCommitHook is implemented by models that need to react to
// bulk Update / Delete / ForceDelete writes once the surrounding
// transaction commits. Unlike per-row [AfterCommitHook], this hook
// fires exactly once per bulk statement and carries the affected
// primary-key set.
//
// The receiver is the zero value of the model type and must not be
// accessed; this hook is invoked once per bulk statement, not once
// per row. Use the ids parameter to identify affected rows.
//
// Single primary key only in v1: composite primary keys return an
// error from the bulk path. Models with composite keys should chain
// [Query.WithRowHooks] instead, which materialises rows and falls
// back to the per-row [AfterCommitHook] contract.
//
// ID-capture strategy is driver-dependent:
//
//   - PostgreSQL: ids are captured atomically by appending RETURNING
//     <pk> to the UPDATE/DELETE itself. There is no race window and
//     no extra round trip; the bulk path emits exactly one
//     QueryExecuted event per statement.
//   - MySQL / SQLite: ids are captured by a SELECT issued before the
//     write. Rows may shift between the SELECT and the write under
//     concurrent traffic, so the id set is a snapshot, not a
//     guarantee. Wrap the call in a Transaction with appropriate
//     isolation when exact fidelity matters. The bulk path emits two
//     QueryExecuted events on these drivers: the pre-capture SELECT
//     and the actual write. Listeners that count queries should
//     filter on op or SQL prefix if double-counting matters.
//
// Pair with [Query.WithBulkLock] to lock the captured rows for the
// transaction's duration on drivers that take the pre-SELECT capture
// path (MySQL, SQLite without the RETURNING wrapper). The flag is a
// no-op on the atomic RETURNING branch (PostgreSQL) because the write
// IS the capture, and a no-op outside a transaction because auto-commit
// releases the lock immediately. SQLite has no row-level locking and
// its grammar never emits FOR UPDATE, so the flag is also a no-op
// there.
//
// Tier C ([Query.WithRowHooks]) always uses the pre-SELECT path on
// every driver because it needs full row hydration, which RETURNING
// pk-only cannot provide.
//
// Zero-row contract: BulkAfterCommit fires once per bulk statement
// even when no rows matched the WHERE; the ids slice is empty in
// that case. Listeners that should only react to non-empty writes
// must inspect len(ids) themselves.
//
// Errors returned by BulkAfterCommit are logged but never propagated
// to the caller; by the time the hook fires the tx has committed.
type BulkAfterCommitHook interface {
	BulkAfterCommit(ctx context.Context, ids []any, op BulkOp) error
}

// pkColumnFor returns the primary-key column name declared on T.
// Returns an error when T has no primary key, or when T has more than
// one primary-key column (composite keys are unsupported by the bulk
// hook path; callers should fall back to Query.WithRowHooks).
func pkColumnFor[T any]() (string, error) {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		return "", fmt.Errorf("velocity/orm: bulk: type T has nil reflect.Type")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	meta := MetaFor(t)
	if meta == nil {
		return "", fmt.Errorf("velocity/orm: bulk: no model meta for %s", t.Name())
	}
	var found string
	for _, c := range meta.Columns() {
		if !c.IsPrimaryKey {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("velocity/orm: bulk: composite primary keys not supported on %s (found %q and %q); use Query.WithRowHooks() instead", t.Name(), found, c.Column)
		}
		found = c.Column
	}
	if found == "" {
		return "", fmt.Errorf("velocity/orm: bulk: no primary key column on %s", t.Name())
	}
	return found, nil
}

// modelBulkAfterCommitHook returns the BulkAfterCommitHook
// implementation for type T (if any). The lookup tries both value and
// pointer receivers so models declaring the method on either form
// resolve.
func modelBulkAfterCommitHook[T any]() (BulkAfterCommitHook, bool) {
	var zero T
	if h, ok := any(&zero).(BulkAfterCommitHook); ok {
		return h, true
	}
	if h, ok := any(zero).(BulkAfterCommitHook); ok {
		return h, true
	}
	return nil, false
}

// selectPrimaryKeys runs SELECT pkCol FROM table WHERE <conditions>
// against drv and returns the captured ids in order. Empty result
// returns a nil slice. The query is built through the driver's
// grammar so identifier quoting and placeholder dialects match the
// surrounding write statement.
//
// lockForUpdate, when true, asks the grammar to emit FOR UPDATE so
// concurrent writers block on the captured rows for the duration of
// the surrounding transaction. The flag is plumbed through to
// [drivers.SelectQuery.LockForUpdate] verbatim; grammars that do not
// support row-level locking (SQLite) silently ignore it.
func selectPrimaryKeys(ctx context.Context, drv drivers.Driver, table, pkCol string, conditions []drivers.Condition, lockForUpdate bool) ([]any, error) {
	if drv == nil {
		return nil, fmt.Errorf("velocity/orm: bulk: nil driver")
	}
	sel := &drivers.SelectQuery{
		Table:         table,
		Columns:       []string{pkCol},
		Conditions:    conditions,
		LockForUpdate: lockForUpdate,
	}
	sqlStr, args := drv.Grammar().CompileSelect(sel)
	rows, err := drv.QueryContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []any
	for rows.Next() {
		var id any
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// dispatchBulkAfterCommit fires hook against the active TxCallbacks
// (registers OnCommit) when there is one, or fires inline (matching
// the auto-commit branch of registerModelAfterCommit) when there is
// no surrounding Transaction. ids may be empty; the call is still
// dispatched so listeners can observe zero-row writes.
func dispatchBulkAfterCommit(ctx context.Context, hook BulkAfterCommitHook, ids []any, op BulkOp) {
	if hook == nil {
		return
	}
	cbs := lookupTxCallbacks(ctx)
	if cbs == nil {
		dispatcher := lookupTxRecoverDispatcher(ctx)
		runCallbackSafe(ctx, func(c context.Context) error {
			return hook.BulkAfterCommit(c, ids, op)
		}, "bulk_after_commit_inline", nil, dispatcher)
		return
	}
	cbs.OnCommit(func(c context.Context) error {
		return hook.BulkAfterCommit(c, ids, op)
	})
}

// bulkHookPlan describes how the bulk-write path must capture affected
// primary keys (or row snapshots) and dispatch the matching hook surface.
//
// On most drivers the plan is fully resolved BEFORE the write: ids are
// SELECTed up front and afterFn closes over them. On PostgreSQL the
// plan defers id capture to the write itself: ReturningPK is non-empty,
// the caller appends RETURNING <pk> to the UPDATE/DELETE, executes via
// QueryContext, and feeds the scanned ids into afterFn. The two paths
// share the same dispatcher contract: afterFn is the only thing the
// caller has to invoke once the write succeeds.
type bulkHookPlan struct {
	// afterFn fires the appropriate hook (Tier B BulkAfterCommitHook
	// dispatch, or Tier C per-row registration). For non-Returning
	// plans the ids argument is ignored; the closure already holds
	// the pre-captured set. For Returning plans the caller scans the
	// statement's RETURNING rowset and passes the ids in.
	afterFn func(ids []any)
	// ReturningPK is the quoted-once-by-grammar primary key column
	// to append after RETURNING when the caller compiles its UPDATE
	// or DELETE. Empty when the plan does not use RETURNING (Tier C
	// always, plus Tier B on non-Postgres drivers, plus the no-hook
	// no-op case).
	ReturningPK string
}

// useReturning reports whether the caller must compile the UPDATE/DELETE
// with the RETURNING clause and execute it via QueryContext to scan the
// returned primary keys.
func (p *bulkHookPlan) useReturning() bool {
	return p != nil && p.ReturningPK != ""
}

// invoke fires afterFn with the supplied ids, ignoring the call when
// there is no hook work to do (zero-value plan or no-hook fast path).
// ids is the rowset captured from RETURNING on Postgres, or nil on
// non-Returning plans (the closure ignores its argument and uses the
// pre-captured set).
func (p *bulkHookPlan) invoke(ids []any) {
	if p == nil || p.afterFn == nil {
		return
	}
	p.afterFn(ids)
}

// bulkPrepareHooks resolves the hook plan for a bulk write. Returns a
// zero-value plan (afterFn == nil) when the call has no hook work to do,
// in which case the caller must skip the post-write invocation.
//
// Two hook surfaces are handled:
//
//   - Tier C ([Query.WithRowHooks]): the matching rows are SELECTed and
//     hydrated into []T immediately. The returned closure wires per-row
//     [AfterCommitHook] / [AfterRollbackHook] against the active
//     [TxCallbacks] (or fires inline on auto-commit). Suppresses Tier B
//     even if the model also implements [BulkAfterCommitHook]. RETURNING
//     pk-only cannot rehydrate full rows, so this tier always uses the
//     pre-SELECT path regardless of driver.
//   - Tier B ([BulkAfterCommitHook]): the matching primary keys are
//     captured and dispatched once per statement. On PostgreSQL the plan
//     opts in to RETURNING, deferring capture to the write itself; the
//     caller compiles RETURNING pk into the UPDATE/DELETE and feeds the
//     scanned ids into the plan. On other drivers the ids are SELECTed
//     up front and the closure closes over them, with the documented
//     race-window caveat.
//
// Pre-fetch order matters on the SELECT path: the SELECT runs before
// the write so the captured set reflects what the write WILL touch (an
// Update that flips a predicate column would mask its own effect on a
// post-fetch).
func (q *Query[T]) bulkPrepareHooks(ctx context.Context, op BulkOp) (bulkHookPlan, error) {
	if q == nil || q.driver == nil {
		return bulkHookPlan{}, nil
	}

	if q.withRowHooks {
		clone := q.Clone()
		clone.preloads = nil
		clone.limit = nil
		clone.offset = nil
		clone.columns = []string{"*"}
		// WithBulkLock opts the row-hydration SELECT into FOR UPDATE.
		// Clone() already propagated withBulkLock, but the lock is
		// applied via lockForUpdate (the storage-layer flag consumed by
		// Get); flipping it here keeps the two surfaces decoupled while
		// honouring the caller's intent on the pre-SELECT path.
		if q.withBulkLock {
			clone.LockForUpdate()
		}
		rows, fetchErr := clone.Get(ctx)
		if fetchErr != nil {
			return bulkHookPlan{}, fetchErr
		}
		return bulkHookPlan{
			afterFn: func(_ []any) {
				for i := range rows {
					registerModelAfterCommit(ctx, &rows[i])
				}
			},
		}, nil
	}

	hook, ok := modelBulkAfterCommitHook[T]()
	if !ok {
		return bulkHookPlan{}, nil
	}
	pkCol, pkErr := pkColumnFor[T]()
	if pkErr != nil {
		return bulkHookPlan{}, pkErr
	}

	// Atomic-capture path: any grammar that implements ReturningGrammar
	// claims atomic ID capture as part of the interface contract (see
	// drivers.ReturningGrammar godoc). We treat the capability assertion
	// as authoritative, no driver-name double-check, so SQLite 3.35+ /
	// MariaDB 10.5+ adapters can opt in by implementing the interface.
	// No pre-SELECT, no race window, exactly one statement and one
	// QueryExecuted event per bulk write.
	if _, hasReturning := q.driver.Grammar().(drivers.ReturningGrammar); hasReturning {
		return bulkHookPlan{
			ReturningPK: pkCol,
			afterFn: func(ids []any) {
				dispatchBulkAfterCommit(ctx, hook, ids, op)
			},
		}, nil
	}

	// Pre-SELECT fallback for drivers without RETURNING support on
	// UPDATE/DELETE (currently MySQL and SQLite without the wrapper).
	// Documented race window applies; see BulkAfterCommitHook godoc.
	// WithBulkLock opts the pre-SELECT into FOR UPDATE so the captured
	// row set is held for the rest of the surrounding transaction; the
	// flag is silently ignored on the atomic RETURNING branch above.
	ids, selErr := selectPrimaryKeys(ctx, q.driver, q.table, pkCol, q.conditions, q.withBulkLock)
	if selErr != nil {
		return bulkHookPlan{}, selErr
	}
	return bulkHookPlan{
		afterFn: func(_ []any) {
			dispatchBulkAfterCommit(ctx, hook, ids, op)
		},
	}, nil
}

// scanReturnedIDs reads a primary-key-only rowset from a RETURNING-
// augmented UPDATE/DELETE into a slice of ids. Always closes the
// rowset; returns whatever it scanned alongside any error so the
// caller can still surface partial captures if it wants to.
func scanReturnedIDs(rows *sql.Rows) ([]any, error) {
	defer rows.Close()
	var ids []any
	for rows.Next() {
		var id any
		if err := rows.Scan(&id); err != nil {
			return ids, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return ids, err
	}
	return ids, nil
}
