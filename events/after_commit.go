package events

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// ShouldDispatchAfterCommit signals that a listener should fire only when the
// surrounding database transaction commits. Listeners that update read
// models, enqueue jobs, send email, invalidate caches, or otherwise produce
// side effects that must be consistent with the persisted row should opt in
// via this interface so a transaction rollback drops the side effect
// instead of letting it land while the row vanishes.
//
// Outside a transaction (no after-commit queue installed on ctx), an opt-in
// listener fires inline exactly as a non-opt-in listener would: the
// interface is a behaviour selector inside Dispatch, not a "this listener
// runs deferred always" marker.
//
// The method returns bool rather than being a marker interface so a listener
// type can decide per-instance whether to defer (e.g. a metrics listener
// that defers writes against persistent counters but fires inline against
// in-memory gauges).
type ShouldDispatchAfterCommit interface {
	// ShouldDispatchAfterCommit reports whether the listener should be
	// deferred until the surrounding transaction commits.
	ShouldDispatchAfterCommit() bool
}

// afterCommitTask is one queued listener invocation. The closure captures
// the producer-side event, listener, and dispatcher so FireAfterCommit can
// run the same code path that the inline branch of Dispatch would have run.
type afterCommitTask struct {
	fn func(ctx context.Context) error
}

// afterCommitQueue accumulates deferred listener invocations during a
// transaction. The queue is mutable via Enqueue and drained by exactly one
// of FireAfterCommit (commit) or DropAfterCommit (rollback). State after
// drain is terminal: subsequent Enqueue calls return false so a late
// dispatch in a finalizer cannot smuggle work past the boundary.
//
// `claimed` distinguishes the two install-time states:
//
//   - false: the queue was attached by PrepareAfterCommit but no
//     InstallAfterCommitQueue has run yet. The first Install call flips
//     `claimed` to true under q.mu and returns an owner handle.
//   - true: the queue is already owned by an outer Install caller.
//     Subsequent Install calls return non-owner handles whose baseline is
//     the current task count (savepoint semantics).
//
// The claim flip closes the M-48 F2 hole where PrepareAfterCommit followed
// by Transaction silently produced an orphan queue: without `claimed`, the
// first Install treated the prepared queue as already-nested and returned
// owner=false, so the Transaction never fired its drain.
type afterCommitQueue struct {
	mu       sync.Mutex
	tasks    []afterCommitTask
	claimed  bool // true once an Install call has taken ownership; gates owner vs nested handle
	draining bool // true while FireAfterCommit is invoking tasks
	finished bool // true once Fire or Drop has completed; Enqueue refuses afterwards
}

// afterCommitKey is the context key under which an *afterCommitQueue is
// stored. Distinct from holderKey/bufferKey so the buffered-event surface
// (events.WithBuffer / events.PrepareBuffer) and the after-commit surface
// can coexist on the same ctx without colliding.
type afterCommitKey struct{}

// AfterCommitHandle is the opaque ownership token returned by
// InstallAfterCommitQueue. It binds together:
//
//   - the queue pointer (so callers cannot lose track of which queue they
//     installed across nested ctx derivations);
//   - the baseline task count captured at install time (used by nested
//     callers to roll back ONLY their own enqueued work on inner rollback,
//     while leaving outer-enqueued tasks intact);
//   - the owner flag (true for the outermost install; false for nested
//     installs that joined an existing queue).
//
// The zero value is a no-op handle: Owner() == false, every method is
// safe to call and does nothing. orm.Manager.Transaction stores the
// handle returned from InstallAfterCommitQueue and dispatches against it:
//
//   - owner == true:  commit -> FireAfterCommit; rollback -> DropAfterCommit.
//   - owner == false: commit -> no-op (outer owns the drain);
//     rollback -> TruncateToBaseline (only inner work).
type AfterCommitHandle struct {
	q        *afterCommitQueue
	baseline int
	owner    bool
}

// Owner reports whether this handle owns the queue's lifecycle. The
// outermost InstallAfterCommitQueue call returns owner=true; every
// nested call on the same ctx returns owner=false. Callers MUST gate
// FireAfterCommit / DropAfterCommit on Owner() so a nested release does
// not prematurely drain listeners that need to wait for the outermost
// commit.
func (h AfterCommitHandle) Owner() bool { return h.owner }

// TruncateToBaseline rolls the queue back to the task count captured at
// install time, dropping every task enqueued under this handle while
// preserving tasks enqueued by outer scopes. Used by nested rollback
// paths (inner Transaction returns an error or panics) so the outer
// Transaction's later commit fires only outer-enqueued work.
//
// On an owner handle TruncateToBaseline is equivalent to DropAfterCommit
// (baseline is 0 by construction), but callers should prefer
// DropAfterCommit at owner level for clarity: TruncateToBaseline is the
// nested-only API and signals savepoint semantics to readers.
//
// Safe to call on the zero handle.
func (h AfterCommitHandle) TruncateToBaseline() {
	if h.q == nil {
		return
	}
	h.q.mu.Lock()
	defer h.q.mu.Unlock()
	if h.q.finished {
		return
	}
	if h.baseline < len(h.q.tasks) {
		h.q.tasks = h.q.tasks[:h.baseline]
	}
}

// PrepareAfterCommit attaches an empty after-commit queue to ctx so a later
// orm.Manager.Transaction call can drain it on commit / rollback. Callers
// MUST use the returned ctx for the subsequent Transaction so the holder
// flows through into the tx body.
//
// PrepareAfterCommit is idempotent: if ctx already carries a queue, the
// same ctx is returned unchanged so nested preparations do not stack
// holders.
//
// The queue is left unclaimed: the first InstallAfterCommitQueue call
// (typically from orm.Manager.Transaction) claims the queue and becomes
// its owner; subsequent installs on the same queue are nested. Without
// this two-step shape a Prepare-then-Transaction sequence would orphan
// the queue (Install would see an existing queue, return owner=false,
// and the Transaction wrapper would never fire the drain).
//
// Outside a Transaction (no orm wiring), the queue still records tasks
// but they will never fire. The orm layer owns the drain because rollback
// and commit boundaries are orm-defined; events alone has no way to know
// which is which.
func PrepareAfterCommit(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(afterCommitKey{}).(*afterCommitQueue); ok {
		return ctx
	}
	return context.WithValue(ctx, afterCommitKey{}, &afterCommitQueue{})
}

// InstallAfterCommitQueue is the entry orm.Manager.Transaction uses to
// install or join an after-commit queue on ctx. Three cases:
//
//   - ctx has no queue: a fresh queue is installed (claimed=true at
//     birth) and the returned ctx carries it. handle.Owner() == true
//     with baseline=0. Auto-install path used when the caller did not
//     PrepareAfterCommit.
//   - ctx already carries a queue AND the queue is unclaimed: the first
//     Install on a Prepare-attached queue claims it (claimed flips
//     under q.mu) and returns handle.Owner() == true with baseline=0.
//     ctx is returned unchanged because the queue is already on it.
//     This is the path that fixes M-48 F2: without the claim flip the
//     Install would have returned a non-owner handle and the
//     Transaction wrapper would never have drained the queue.
//   - ctx already carries a queue AND the queue is already claimed:
//     nested Install. ctx is returned unchanged; the handle binds to
//     the existing queue with the current task count captured as the
//     baseline. handle.Owner() == false: the caller MUST NOT call
//     Fire/Drop; instead, on inner rollback / panic the caller invokes
//     handle.TruncateToBaseline to drop only inner-enqueued tasks while
//     leaving outer-enqueued tasks intact. Inner commit is a no-op so
//     forwarding is owned by the outermost scope.
//
// The claim flip and the baseline read both happen under q.mu so two
// concurrent Install calls cannot both believe they own the queue: the
// loser sees claimed=true on its turn under the lock and falls through
// to the nested branch with a baseline of whatever the winner enqueued
// in between.
//
// The baseline+truncate shape closes the rollback-leak hole that the
// previous (ctx, bool) signature had: a nested Transaction that
// enqueued an after-commit task and then returned an error left the
// task in the queue, and the outer commit would fire it from a
// rolled-back savepoint.
//
// Used by orm.Manager.Transaction to wire automatic deferral of
// ShouldDispatchAfterCommit listeners whether or not the caller invoked
// PrepareAfterCommit on the outer ctx.
func InstallAfterCommitQueue(ctx context.Context) (context.Context, AfterCommitHandle) {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := ctx.Value(afterCommitKey{}).(*afterCommitQueue); ok && existing != nil {
		existing.mu.Lock()
		if !existing.claimed {
			// First Install on a Prepare-attached queue: claim it and
			// become the owner. Baseline is 0 because the queue must
			// not have accumulated tasks before any owner existed
			// (Dispatch's gate requires HasAfterCommitQueue, which is
			// true here, but tasks queued under an unclaimed queue
			// without a Transaction wrapper would never fire anyway).
			existing.claimed = true
			existing.mu.Unlock()
			return ctx, AfterCommitHandle{q: existing, baseline: 0, owner: true}
		}
		baseline := len(existing.tasks)
		existing.mu.Unlock()
		return ctx, AfterCommitHandle{q: existing, baseline: baseline, owner: false}
	}
	q := &afterCommitQueue{claimed: true}
	return context.WithValue(ctx, afterCommitKey{}, q), AfterCommitHandle{q: q, baseline: 0, owner: true}
}

// HasAfterCommitQueue reports whether ctx carries an active after-commit
// queue. Active means installed AND not yet fired/dropped: once
// FireAfterCommit or DropAfterCommit completes, HasAfterCommitQueue
// returns false so a follow-on Dispatch fires inline.
//
// Used by Dispatcher.Dispatch to decide whether to gate
// ShouldDispatchAfterCommit listeners.
func HasAfterCommitQueue(ctx context.Context) bool {
	q := lookupAfterCommitQueue(ctx)
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.finished
}

// EnqueueAfterCommit appends a task to the after-commit queue attached to
// ctx. Returns true if the task was queued, false if ctx has no queue or
// the queue has already been fired/dropped. The latter case is the gate's
// signal to fall through to inline execution: a missing queue means no
// transaction, an exhausted queue means the boundary has already passed.
//
// fn receives the ctx that was passed to FireAfterCommit so listeners see
// commit-time context values (a fresh deadline, post-tx logger fields,
// etc.) rather than the in-flight tx ctx that is no longer valid.
func EnqueueAfterCommit(ctx context.Context, fn func(ctx context.Context) error) bool {
	if fn == nil {
		return false
	}
	q := lookupAfterCommitQueue(ctx)
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.finished || q.draining {
		// finished: boundary already passed. draining: we're inside
		// FireAfterCommit; a listener fired re-entrantly. Either way
		// the caller should fire inline instead of stacking a task
		// that would never run.
		return false
	}
	q.tasks = append(q.tasks, afterCommitTask{fn: fn})
	return true
}

// FireAfterCommit drains every queued task in order, returning the joined
// error from any task that failed. Tasks that panic have their panic
// converted to an error via panicerr.FromRecovered so a single misbehaving
// listener cannot take down the orm commit path.
//
// After Fire returns, the queue is in a terminal "finished" state: further
// EnqueueAfterCommit calls return false so a stray Dispatch in a finalizer
// fires inline instead of silently disappearing.
//
// Re-entrant Dispatch calls invoked from inside a task body fire inline
// (HasAfterCommitQueue reports false during drain) so the listener cannot
// loop back into the queue and stall the drain.
//
// orm.Manager.Transaction calls this on a successful commit when ctx
// carries a queue. Application code typically does not invoke it directly.
func FireAfterCommit(ctx context.Context) error {
	q := lookupAfterCommitQueue(ctx)
	if q == nil {
		return nil
	}
	q.mu.Lock()
	if q.finished {
		q.mu.Unlock()
		return nil
	}
	q.draining = true
	tasks := q.tasks
	q.tasks = nil
	q.mu.Unlock()

	var errs []error
	for _, task := range tasks {
		if err := safeInvoke(ctx, task.fn); err != nil {
			errs = append(errs, err)
		}
	}

	q.mu.Lock()
	q.draining = false
	q.finished = true
	q.mu.Unlock()
	return errors.Join(errs...)
}

// DropAfterCommit discards every queued task without invoking any of them
// and marks the queue terminal. After Drop returns, EnqueueAfterCommit
// refuses further work so a stray Dispatch in a finalizer fires inline
// rather than queueing onto a dead queue.
//
// orm.Manager.Transaction calls this on rollback (explicit error return,
// commit failure, or panic). Application code typically does not invoke
// it directly.
func DropAfterCommit(ctx context.Context) {
	q := lookupAfterCommitQueue(ctx)
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.tasks = nil
	q.finished = true
}

// PendingAfterCommit returns the count of queued tasks not yet drained.
// Intended for assertions in tests; the value is racy by construction when
// concurrent producers are still enqueueing work.
func PendingAfterCommit(ctx context.Context) int {
	q := lookupAfterCommitQueue(ctx)
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.tasks)
}

// lookupAfterCommitQueue returns the queue attached to ctx via
// PrepareAfterCommit, or nil if no queue is set.
func lookupAfterCommitQueue(ctx context.Context) *afterCommitQueue {
	if ctx == nil {
		return nil
	}
	q, _ := ctx.Value(afterCommitKey{}).(*afterCommitQueue)
	return q
}

// safeInvoke runs fn under a panic recover and returns the recovered value
// converted to an error. Used by FireAfterCommit so one panicking listener
// does not abort the drain of the remaining queued listeners.
func safeInvoke(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			if existing := panicerr.FromRecovered(p); existing != nil {
				err = fmt.Errorf("after-commit listener panicked: %w", existing)
			} else {
				err = fmt.Errorf("after-commit listener panicked: %v", p)
			}
		}
	}()
	return fn(ctx)
}
