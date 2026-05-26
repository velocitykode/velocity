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
type afterCommitQueue struct {
	mu       sync.Mutex
	tasks    []afterCommitTask
	draining bool // true while FireAfterCommit is invoking tasks
	finished bool // true once Fire or Drop has completed; Enqueue refuses afterwards
}

// afterCommitKey is the context key under which an *afterCommitQueue is
// stored. Distinct from holderKey/bufferKey so the buffered-event surface
// (events.WithBuffer / events.PrepareBuffer) and the after-commit surface
// can coexist on the same ctx without colliding.
type afterCommitKey struct{}

// PrepareAfterCommit attaches an empty after-commit queue to ctx so a later
// orm.Manager.Transaction call can drain it on commit / rollback. Callers
// MUST use the returned ctx for the subsequent Transaction so the holder
// flows through into the tx body.
//
// PrepareAfterCommit is idempotent: if ctx already carries a queue, the
// same ctx is returned unchanged so nested preparations do not stack
// holders.
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
// install or join an after-commit queue on ctx. Behaviour mirrors
// InstallBuffer:
//
//   - If ctx has no queue, a fresh queue is installed and the returned
//     ctx carries it. owner == true: the caller is responsible for
//     calling FireAfterCommit on commit and DropAfterCommit on rollback.
//   - If ctx already carries a queue (a nested Transaction reusing the
//     outer queue), the ctx is returned unchanged. owner == false: the
//     caller MUST NOT call Fire/Drop; the outer Transaction owns the
//     drain so a savepoint release does not prematurely flush listeners
//     that need to wait for the outermost commit.
//
// Used by orm.Manager.Transaction to wire automatic deferral of
// ShouldDispatchAfterCommit listeners even when the user did not call
// PrepareAfterCommit on the outer ctx.
func InstallAfterCommitQueue(ctx context.Context) (context.Context, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Value(afterCommitKey{}).(*afterCommitQueue); ok {
		return ctx, false
	}
	return context.WithValue(ctx, afterCommitKey{}, &afterCommitQueue{}), true
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
