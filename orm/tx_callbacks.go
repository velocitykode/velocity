package orm

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/velocitykode/velocity/async"
)

// TxCallback is the signature for callbacks registered against a
// transaction's commit or rollback boundary. Callbacks receive the
// outer Transaction's ctx (so values like trace IDs propagate) and
// may return an error that is logged but never re-raised. By the
// time a callback runs the tx has already terminated, so unwinding
// to the original caller is no longer possible.
//
// Cancellation: the ctx supplied to a callback is detached from the
// surrounding Transaction's ctx via context.WithoutCancel before the
// callback runs. Callbacks observe the same values (auth, trace IDs)
// but are NOT affected by the parent ctx being canceled. The parent
// ctx is most commonly canceled by the same condition that triggered
// the rollback (deadline, request abort), so propagating cancellation
// would poison every subsequent post-commit / post-rollback hook.
type TxCallback func(ctx context.Context) error

// TxCommitFailureCallback fires when tx.Commit returns an error.
// Commit failures leave the transaction in an AMBIGUOUS state: the
// database may have committed the data but the network failed before
// the client received the OK, OR the commit may have been rejected
// outright. A commit-failure callback receives the commit error so
// it can branch on driver-specific error codes (e.g. inspect
// pq.Error.Code, lib/pq's CommitNotConfirmed, or libpq SQLSTATE)
// before deciding to re-enqueue jobs or invalidate caches.
//
// Default behavior of an OnCommitFailure callback should be to LOG
// the ambiguity and leave outboxes / caches untouched. This is the
// safe default: rolling back a state that may have actually committed
// risks duplicate work (re-enqueue of jobs that already fired) or
// stale-cache reads (invalidation for changes that did NOT land).
//
// Commit-failure callbacks are NOT rollback callbacks. Rollback
// callbacks fire only when a rollback is confirmed (the closure
// returned an error and tx.Rollback succeeded, or a panic-driven
// rollback succeeded). On commit error, only the failure callbacks
// fire; never the rollback list.
type TxCommitFailureCallback func(ctx context.Context, commitErr error) error

// TxCallbacks accumulates callbacks registered during a transaction
// and drains them after the tx terminates. A fresh TxCallbacks is
// installed by Manager.Transaction; user code reaches it through
// orm.OnCommit / orm.OnRollback / orm.OnCommitFailure (which look up
// the holder on ctx) or through the lifecycle hook interfaces
// (AfterCommitHook / AfterRollbackHook) on saved models.
//
// Callbacks fire in registration order. A panic inside a callback is
// recovered, surfaced via the Manager's event dispatcher (TxRecover),
// and logged through the configured logger when one is wired;
// subsequent callbacks still run so a single bad callback cannot
// block outbox / cache invalidation work registered after it.
//
// Concurrency: TxCallbacks is mutex-protected so concurrent
// registrations from goroutines fanned out inside the Transaction
// closure are safe. Drain methods execute callbacks while holding no
// lock so callbacks can register further callbacks (which queue onto
// the same slice and are drained in the same pass).
type TxCallbacks struct {
	mu             sync.Mutex
	commit         []TxCallback
	rollback       []TxCallback
	commitFailure  []TxCommitFailureCallback
	committed      bool
	rolled         bool
	commitFailedAt bool
	// dispatcher is invoked to surface panics that occur inside a
	// callback so observability pipelines see the failure even when
	// no logger is wired. Set by Manager.Transaction at install time.
	// May be nil; see runCallbackSafe for the fallback path.
	dispatcher func(*TxRecover)
}

// OnCommit appends fn to the commit callback list. fn fires after
// the surrounding tx commits successfully. If the tx has already
// terminated (committed, rolled back, or commit-failed), the
// registration is a no-op: callers must register before the
// Transaction closure returns. Safe to call from any goroutine.
func (c *TxCallbacks) OnCommit(fn TxCallback) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committed || c.rolled || c.commitFailedAt {
		return
	}
	c.commit = append(c.commit, fn)
}

// OnRollback appends fn to the rollback callback list. fn fires
// after the surrounding tx is rolled back via an explicit error
// return from the closure or a panic-driven rollback. fn does NOT
// fire on commit-error: commit-error leaves the tx in an ambiguous
// state and is handled by OnCommitFailure callbacks. Safe to call
// from any goroutine.
func (c *TxCallbacks) OnRollback(fn TxCallback) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committed || c.rolled || c.commitFailedAt {
		return
	}
	c.rollback = append(c.rollback, fn)
}

// OnCommitFailure appends fn to the commit-failure callback list.
// fn fires when tx.Commit returns an error, leaving the tx in an
// ambiguous state. fn receives the commit error so app-specific
// error-code inspection (driver SQLSTATE, lib/pq CommitNotConfirmed,
// etc.) can drive the response: log-and-leave-alone is the safe
// default, retrying or aggressive rollback-style cleanup is unsafe
// without confirmation that the commit truly did not land.
func (c *TxCallbacks) OnCommitFailure(fn TxCommitFailureCallback) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.committed || c.rolled || c.commitFailedAt {
		return
	}
	c.commitFailure = append(c.commitFailure, fn)
}

// CommitCount returns the number of registered commit callbacks.
// Test-friendly accessor; production code should not depend on it.
func (c *TxCallbacks) CommitCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.commit)
}

// RollbackCount returns the number of registered rollback callbacks.
// Test-friendly accessor; production code should not depend on it.
func (c *TxCallbacks) RollbackCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.rollback)
}

// CommitFailureCount returns the number of registered commit-failure
// callbacks. Test-friendly accessor; production code should not
// depend on it.
func (c *TxCallbacks) CommitFailureCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.commitFailure)
}

// setDispatcher wires a panic-surface dispatcher onto the callbacks
// list. Manager.Transaction calls this immediately after install so
// runCallbackSafe can route hook panics to the same TxRecover event
// stream the tx body uses.
func (c *TxCallbacks) setDispatcher(fn func(*TxRecover)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dispatcher = fn
}

// txRecoverDispatcherKey is the ctx slot Manager.Save uses to make its
// TxRecover dispatcher reachable from registerModelAfterCommit's
// auto-commit (no-Transaction) branch. Without this slot the inline
// runCallbackSafe call would receive nil dispatcher + nil logger and
// a panic in the AfterCommit hook would only land on os.Stderr - the
// observability sink wired to *Manager would never see it.
type txRecoverDispatcherKey struct{}

// withTxRecoverDispatcher attaches dispatch to ctx so the inline
// AfterCommit-on-auto-commit path can route a hook panic through the
// same TxRecover dispatcher Manager.Transaction uses for its own
// callback list. Idempotent: re-wrapping with a non-nil dispatch
// overrides the previous value.
func withTxRecoverDispatcher(ctx context.Context, dispatch func(*TxRecover)) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if dispatch == nil {
		return ctx
	}
	return context.WithValue(ctx, txRecoverDispatcherKey{}, dispatch)
}

// lookupTxRecoverDispatcher returns the dispatcher attached to ctx by
// withTxRecoverDispatcher, or nil when no Manager has stamped one
// (e.g. tests using saveWithDriverCtx directly with a bare ctx).
func lookupTxRecoverDispatcher(ctx context.Context) func(*TxRecover) {
	if ctx == nil {
		return nil
	}
	if fn, ok := ctx.Value(txRecoverDispatcherKey{}).(func(*TxRecover)); ok {
		return fn
	}
	return nil
}

// runCommit drains commit callbacks in registration order. Each
// callback runs under a recover so a panic does not leave the tx
// state half-cleaned; failures are logged through logger when set,
// and routed through the dispatcher's TxRecover event when configured.
//
// The ctx supplied to each callback is detached from cancellation via
// context.WithoutCancel: by the time post-commit hooks fire, the row
// is durable and a parent-ctx deadline (e.g. the request that drove
// the commit timing out mid-Commit) must NOT poison the cache /
// outbox cascade. Callbacks still observe trace IDs and request-scoped
// values from the parent.
func (c *TxCallbacks) runCommit(ctx context.Context, logger eventLogger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.committed || c.rolled || c.commitFailedAt {
		c.mu.Unlock()
		return
	}
	c.committed = true
	hookCtx := context.WithoutCancel(ctx)
	dispatcher := c.dispatcher
	// Iterate by re-reading the slice each step so callbacks that
	// register further OnCommit work inside their own body still run
	// in this pass. We snapshot the index, not the slice, and re-read
	// length on every iteration under the lock.
	for i := 0; ; i++ {
		if i >= len(c.commit) {
			break
		}
		fn := c.commit[i]
		c.mu.Unlock()
		runCallbackSafe(hookCtx, fn, "after_commit", logger, dispatcher)
		c.mu.Lock()
	}
	c.commit = nil
	c.rollback = nil
	c.commitFailure = nil
	c.mu.Unlock()
}

// runRollback drains rollback callbacks in registration order. Same
// recover / logging contract as runCommit.
//
// Rollback callbacks fire when the tx is CONFIRMED rolled back (the
// closure returned an error and tx.Rollback succeeded, or a panic
// triggered the rollback path). They do NOT fire on commit-error,
// where the tx state is ambiguous; OnCommitFailure callbacks handle
// that case.
//
// Cancellation is detached: OnRollback is most often called precisely
// because the parent ctx was canceled (deadline / request abort),
// so propagating that cancellation would poison the rollback cascade.
func (c *TxCallbacks) runRollback(ctx context.Context, logger eventLogger) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.committed || c.rolled || c.commitFailedAt {
		c.mu.Unlock()
		return
	}
	c.rolled = true
	hookCtx := context.WithoutCancel(ctx)
	dispatcher := c.dispatcher
	for i := 0; ; i++ {
		if i >= len(c.rollback) {
			break
		}
		fn := c.rollback[i]
		c.mu.Unlock()
		runCallbackSafe(hookCtx, fn, "after_rollback", logger, dispatcher)
		c.mu.Lock()
	}
	c.commit = nil
	c.rollback = nil
	c.commitFailure = nil
	c.mu.Unlock()
}

// runCommitFailure drains commit-failure callbacks in registration
// order. Fired when tx.Commit returns an error; the tx is in an
// ambiguous state (the database may have committed but the client
// did not see the OK). Each callback receives the commit error so
// driver-specific error-code branching is possible.
//
// Same recover / dispatcher / logger contract as runCommit. Same
// WithoutCancel ctx detaching as runCommit / runRollback.
func (c *TxCallbacks) runCommitFailure(ctx context.Context, logger eventLogger, commitErr error) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.committed || c.rolled || c.commitFailedAt {
		c.mu.Unlock()
		return
	}
	c.commitFailedAt = true
	hookCtx := context.WithoutCancel(ctx)
	dispatcher := c.dispatcher
	for i := 0; ; i++ {
		if i >= len(c.commitFailure) {
			break
		}
		fn := c.commitFailure[i]
		c.mu.Unlock()
		wrapped := func(c context.Context) error { return fn(c, commitErr) }
		runCallbackSafe(hookCtx, wrapped, "after_commit_failure", logger, dispatcher)
		c.mu.Lock()
	}
	c.commit = nil
	c.rollback = nil
	c.commitFailure = nil
	c.mu.Unlock()
}

// runCallbackSafe executes fn under a recover. Errors and panics are
// surfaced through three sinks (in order of preference):
//
//  1. The configured logger (fakeLogger.Error / log.Logger.Error)
//     receives a structured "tx callback panicked" entry.
//  2. The dispatcher (Manager event dispatcher, when wired) receives
//     a TxRecover event with Cause="callback_panic" so observability
//     pipelines correlate the failure even when no logger is set.
//  3. When BOTH logger and dispatcher are nil, a one-line panic notice
//     is written to os.Stderr so the failure is never silently
//     swallowed. This is the last-resort sink and is only reached
//     when the Manager is constructed without a logger and without
//     an event dispatcher (typically only in tests).
//
// By the time runCallbackSafe is called, the surrounding transaction
// has already committed, rolled back, or commit-failed, so unwinding
// to the caller is no longer possible. Panics here cannot affect the
// surrounding transaction but a misbehaving callback must not block
// siblings, so each callback is run inside its own runCallbackSafe
// scope.
//
// Panic recovery routes through async.FromRecovered so the recovered
// value is wrapped with the framework's standard panic-to-error
// adapter (carrying the original value plus a stack frame). This
// keeps callback panic logs symmetric with goroutine panic logs
// elsewhere in the framework.
func runCallbackSafe(ctx context.Context, fn TxCallback, phase string, logger eventLogger, dispatcher func(*TxRecover)) {
	defer func() {
		if p := recover(); p != nil {
			wrapped := async.FromRecovered(p)
			if logger != nil {
				logger.Error("velocity/orm: tx callback panicked",
					"phase", phase, "error", wrapped)
			}
			if dispatcher != nil {
				dispatcher(&TxRecover{
					Cause:      "callback_panic",
					PanicValue: fmt.Sprintf("%s: %v", phase, p),
				})
			}
			if logger == nil && dispatcher == nil {
				// Last-resort sink: at least one observer must see the
				// panic. Stderr is universally available and never
				// silenced by the framework.
				fmt.Fprintf(os.Stderr, "velocity/orm: tx callback panicked phase=%s error=%v\n", phase, wrapped)
			}
		}
	}()
	if err := fn(ctx); err != nil && logger != nil {
		logger.Warn("velocity/orm: tx callback returned error",
			"phase", phase, "error", err.Error())
	}
}

// txCallbacksHolder is the slot stored on ctx that PrepareTxCallbacks
// fills so subsequent OnCommit / OnRollback / lookupTxCallbacks calls
// can find the active TxCallbacks for the surrounding Transaction.
//
// The holder slot indirection (rather than storing the *TxCallbacks
// directly via context.WithValue) lets Manager.Transaction install
// and release the active callback list without rebuilding the ctx
// value chain, matching the approach used by events.PrepareBuffer
// for the per-tx event buffer so the fn signature passed to
// Transaction does not have to change.
type txCallbacksHolder struct {
	mu  sync.RWMutex
	cbs *TxCallbacks
}

func (h *txCallbacksHolder) load() *TxCallbacks {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cbs
}

func (h *txCallbacksHolder) store(c *TxCallbacks) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cbs = c
}

type txCallbacksHolderKey struct{}

// PrepareTxCallbacks attaches a mutable callbacks holder slot to ctx
// so a later orm.Manager.Transaction call can install a per-tx
// TxCallbacks without requiring the user's tx callback signature to
// change. Callers MUST use the returned ctx for both Transaction and
// any subsequent orm.OnCommit / orm.OnRollback / orm.OnCommitFailure
// lookups.
//
// PrepareTxCallbacks is idempotent: if ctx already carries a holder,
// the same ctx is returned unchanged.
func PrepareTxCallbacks(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(txCallbacksHolderKey{}).(*txCallbacksHolder); ok {
		return ctx
	}
	return context.WithValue(ctx, txCallbacksHolderKey{}, &txCallbacksHolder{})
}

// lookupTxCallbacks returns the *TxCallbacks attached to ctx (via a
// prepared holder slot) or nil when none is active. Used by the
// model save path and the public OnCommit / OnRollback helpers.
func lookupTxCallbacks(ctx context.Context) *TxCallbacks {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(txCallbacksHolderKey{}).(*txCallbacksHolder)
	if h == nil {
		return nil
	}
	return h.load()
}

// installTxCallbacks fills the holder slot on ctx with a fresh
// TxCallbacks. Returns the callbacks list, a boolean indicating
// ownership (true = this call installed the list and owns its
// drain), and a release closure that the Transaction wrapper
// invokes on exit so the holder slot is cleared once the tx
// terminates. When ctx has no holder, a standalone TxCallbacks is
// returned with owner=true (still drained correctly by the
// Transaction wrapper) and release is a no-op. This matches
// events.InstallBuffer behaviour so Transaction stays correct even
// when callers forget PrepareTxCallbacks.
//
// Nested Transaction reuses the outermost callbacks list with
// owner=false so OnCommit / OnRollback registrations inside the
// savepoint scope fire only when the outermost tx terminates
// (matching the buffered-event nesting contract). The inner
// Transaction's drain step becomes a no-op when owner=false because
// it does not own the callbacks list.
func installTxCallbacks(ctx context.Context) (cbs *TxCallbacks, owner bool, release func()) {
	if ctx == nil {
		return &TxCallbacks{}, true, func() {}
	}
	holder, _ := ctx.Value(txCallbacksHolderKey{}).(*txCallbacksHolder)
	if holder == nil {
		return &TxCallbacks{}, true, func() {}
	}
	if existing := holder.load(); existing != nil {
		// Nested Transaction: hand back the outer callbacks so inner
		// registrations queue onto the outer list. Release is a
		// no-op because the outer Transaction owns the holder
		// lifecycle, and owner=false signals to the inner caller
		// that drain must be deferred to the outer.
		return existing, false, func() {}
	}
	cbs = &TxCallbacks{}
	holder.store(cbs)
	return cbs, true, func() { holder.store(nil) }
}

// OnCommit registers fn to fire after the active transaction's
// successful commit. It is the package-level convenience for code
// that has access to ctx but not to the *TxCallbacks directly.
//
// When ctx carries no active TxCallbacks (no surrounding
// Transaction, or PrepareTxCallbacks was not called on the ctx
// passed to Transaction), OnCommit returns ErrNoTxCallbacks so
// callers can fall back to running fn inline if that is acceptable.
//
// Example:
//
//	ctx = orm.PrepareTxCallbacks(ctx)
//	_ = m.Transaction(ctx, func(tx *sql.Tx) error {
//	    if _, err := (Order{}).WithTx(tx).WithContext(ctx).Create(data); err != nil {
//	        return err
//	    }
//	    return orm.OnCommit(ctx, func(ctx context.Context) error {
//	        cache.Forget(ctx, "orders:"+order.ID)
//	        return nil
//	    })
//	})
func OnCommit(ctx context.Context, fn TxCallback) error {
	if fn == nil {
		return nil
	}
	cbs := lookupTxCallbacks(ctx)
	if cbs == nil {
		return ErrNoTxCallbacks
	}
	cbs.OnCommit(fn)
	return nil
}

// OnRollback registers fn to fire after the active transaction is
// rolled back. Symmetric with OnCommit. Does NOT fire on commit-error;
// register OnCommitFailure for that path.
func OnRollback(ctx context.Context, fn TxCallback) error {
	if fn == nil {
		return nil
	}
	cbs := lookupTxCallbacks(ctx)
	if cbs == nil {
		return ErrNoTxCallbacks
	}
	cbs.OnRollback(fn)
	return nil
}

// OnCommitFailure registers fn to fire when tx.Commit returns an
// error. The tx is in an ambiguous state (the database may or may
// not have committed; the network may have failed before the OK was
// received), so fn receives the commit error and should branch on
// driver-specific error codes before deciding whether to invalidate
// caches or re-enqueue jobs. The safe default is to log the
// ambiguity and leave outboxes / caches alone.
//
// On commit error, ONLY OnCommitFailure callbacks fire; OnRollback
// callbacks are NOT invoked because the rollback is not confirmed.
// Symmetric with OnCommit / OnRollback otherwise.
func OnCommitFailure(ctx context.Context, fn TxCommitFailureCallback) error {
	if fn == nil {
		return nil
	}
	cbs := lookupTxCallbacks(ctx)
	if cbs == nil {
		return ErrNoTxCallbacks
	}
	cbs.OnCommitFailure(fn)
	return nil
}

// AfterCommitHook is implemented by models that need work to fire
// after the surrounding transaction commits: outbox dispatch, cache
// invalidation, webhook fanout, anything that requires durability
// before the side effect can be safely observed.
//
// The hook fires only when the model is saved inside a Transaction
// (created via Manager.Transaction with a ctx prepared by
// PrepareTxCallbacks and propagated through Query.WithContext). When
// no active Transaction is detected, the save auto-commits
// implicitly, so the hook fires inline immediately after the in-tx
// hooks (BeforeCreate / AfterCreate or BeforeUpdate / AfterUpdate)
// so the contract is uniform from the model's perspective.
//
// Errors returned by AfterCommit are logged but never propagated to
// the original caller. By the time AfterCommit runs, the tx has
// already committed and the row is durable.
//
// Bulk write asymmetry: this hook is per-row. The bulk paths
// Query.Update, Query.Delete, and Query.ForceDelete translate to a
// single UPDATE/DELETE statement and do NOT fire AfterCommit per
// affected row (matches GORM, Bun, and ent). Two opt-ins:
//   - Implement BulkAfterCommitHook for one event per bulk statement
//     carrying the affected primary-key set.
//   - Call Query.WithRowHooks() to pre-select the rows, hydrate them,
//     and fan out per-row hooks at the cost of an extra SELECT and
//     N model allocations.
type AfterCommitHook interface {
	AfterCommit(ctx context.Context) error
}

// AfterRollbackHook is implemented by models that need to react to
// the surrounding transaction rolling back: clearing in-memory
// state, releasing reservations held outside the database, etc.
//
// The hook fires only when the model is saved inside a Transaction
// that ultimately rolls back. Outside a Transaction the hook never
// fires (the implicit auto-commit cannot roll back). It does NOT
// fire on commit-error: that path is ambiguous; install an
// OnCommitFailure callback when commit-failure observability is
// required.
//
// Bulk write asymmetry: see AfterCommitHook docs. Bulk Update /
// Delete / ForceDelete skip per-row AfterRollback. Use
// BulkAfterCommitHook (with an OnRollback callback registered on the
// active TxCallbacks) or Query.WithRowHooks() to opt in.
type AfterRollbackHook interface {
	AfterRollback(ctx context.Context) error
}

// registerModelAfterCommit wires a saved model's AfterCommit /
// AfterRollback hooks into the active TxCallbacks (if any). Called
// by saveModel / saveUUIDModel / saveImmutableModel after the row
// has been written so the hook captures the committed identity
// (id, timestamps).
//
// Without an active TxCallbacks (no surrounding Transaction), the
// AfterCommit hook is fired inline because the implicit auto-commit
// already happened by the time the save returns. AfterRollback is
// not fired in that case (there is nothing to roll back).
func registerModelAfterCommit(ctx context.Context, model any) {
	if model == nil {
		return
	}
	commitHook, hasCommit := model.(AfterCommitHook)
	rollbackHook, hasRollback := model.(AfterRollbackHook)
	if !hasCommit && !hasRollback {
		return
	}
	cbs := lookupTxCallbacks(ctx)
	if cbs == nil {
		// Outside a Transaction: the surrounding statement already
		// auto-committed. Fire AfterCommit inline so the hook
		// contract is uniform; skip AfterRollback (the implicit
		// auto-commit cannot roll back).
		//
		// Plumb the Manager's TxRecover dispatcher via ctx so a hook
		// panic surfaces a TxRecover event identical to the
		// in-Transaction path. Without this the inline branch would
		// silently drop panics to os.Stderr only.
		if hasCommit {
			dispatcher := lookupTxRecoverDispatcher(ctx)
			runCallbackSafe(ctx, func(c context.Context) error {
				return commitHook.AfterCommit(c)
			}, "after_commit_inline", nil, dispatcher)
		}
		return
	}
	if hasCommit {
		cbs.OnCommit(func(c context.Context) error {
			return commitHook.AfterCommit(c)
		})
	}
	if hasRollback {
		cbs.OnRollback(func(c context.Context) error {
			return rollbackHook.AfterRollback(c)
		})
	}
}
