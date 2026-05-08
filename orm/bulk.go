package orm

import (
	"context"
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
// primary-key set captured by a SELECT issued before the write.
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
// Race window: ids are captured by a SELECT issued before the
// UPDATE/DELETE. Rows may shift between the SELECT and the write
// under concurrent traffic, so the id set is a snapshot, not a
// guarantee. Wrap the call in a Transaction with appropriate
// isolation when exact fidelity matters.
//
// Observability side effect: tables whose model implements this hook
// emit two QueryExecuted events per bulk Update / Delete /
// ForceDelete: the pre-capture SELECT and the actual write.
// Listeners that count queries should filter on op or SQL prefix if
// double-counting matters.
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
func selectPrimaryKeys(ctx context.Context, drv drivers.Driver, table, pkCol string, conditions []drivers.Condition) ([]any, error) {
	if drv == nil {
		return nil, fmt.Errorf("velocity/orm: bulk: nil driver")
	}
	sel := &drivers.SelectQuery{
		Table:      table,
		Columns:    []string{pkCol},
		Conditions: conditions,
	}
	sql, args := drv.Grammar().CompileSelect(sel)
	rows, err := drv.QueryContext(ctx, sql, args...)
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

// bulkPrepareHooks captures whatever the bulk-write hook surface needs
// BEFORE the write and returns a closure that should be invoked after a
// successful write. Returns (nil, nil) when the call has no hook work
// to do, in which case the caller must skip the post-write invocation.
//
// Two hook surfaces are handled:
//
//   - Tier C ([Query.WithRowHooks]): the matching rows are SELECTed and
//     hydrated into []T immediately. The returned closure wires per-row
//     [AfterCommitHook] / [AfterRollbackHook] against the active
//     [TxCallbacks] (or fires inline on auto-commit). Suppresses Tier B
//     even if the model also implements [BulkAfterCommitHook].
//   - Tier B ([BulkAfterCommitHook]): the matching primary keys are
//     SELECTed into []any. The returned closure dispatches one event
//     per bulk statement carrying the captured ids and the supplied op.
//
// Pre-fetch order matters: the SELECT runs before the write so the
// captured set reflects what the write WILL touch (an Update that flips
// a predicate column would mask its own effect on a post-fetch).
//
// The returned closure is safe to invoke even when no callbacks were
// registered (no-ops on empty hook state).
func (q *Query[T]) bulkPrepareHooks(ctx context.Context, op BulkOp) (afterFn func(), err error) {
	if q == nil || q.driver == nil {
		return nil, nil
	}

	if q.withRowHooks {
		clone := q.Clone()
		clone.preloads = nil
		clone.limit = nil
		clone.offset = nil
		clone.columns = []string{"*"}
		rows, fetchErr := clone.Get(ctx)
		if fetchErr != nil {
			return nil, fetchErr
		}
		return func() {
			for i := range rows {
				registerModelAfterCommit(ctx, &rows[i])
			}
		}, nil
	}

	hook, ok := modelBulkAfterCommitHook[T]()
	if !ok {
		return nil, nil
	}
	pkCol, pkErr := pkColumnFor[T]()
	if pkErr != nil {
		return nil, pkErr
	}
	ids, selErr := selectPrimaryKeys(ctx, q.driver, q.table, pkCol, q.conditions)
	if selErr != nil {
		return nil, selErr
	}
	return func() {
		dispatchBulkAfterCommit(ctx, hook, ids, op)
	}, nil
}
