package schemes

import (
	"errors"

	"github.com/velocitykode/velocity/auth"
)

// The request's authentication operations (Login, Logout, the remember-me
// recall and burn that a read of the user may run, and the session
// middleware's commit) take turns through a gate on the request's
// sessionHolder. The gate never waits: an operation reserves it under the
// holder's short mutex, runs the user code it calls (user store, session
// store, server session store, CSRF rotator, remember-token swaps,
// loggers) with no lock held, and publishes what it changed in the holder
// (its transitions and the credential writes it queued) in one step under
// the mutex when it ends. An operation that finds the gate reserved, a
// competing goroutine of the request or a store calling back into the
// scheme for the request it serves, fails closed with
// auth.ErrOperationInProgress: no user, Check false, the error wherever
// the method returns one.
//
// The session object itself (auth.Session, which a custom SessionStore
// implements) is still changed in place by the operation that holds the
// gate: Regenerate, the user id, Invalidate. What makes that safe is that
// nothing reads or saves it halfway: every scheme read of the user fails
// closed while the gate is reserved, and the commit never saves while an
// operation holds the gate, nor after one was torn (unwound by a panic
// after it began changing the session), until a later sign-in or logout
// of the request publishes a whole state again. A handler reading the raw
// session object concurrently with its own Login is outside this.

// errOperationTorn is what the commit returns when an authentication
// operation of the request was unwound by a panic after it began changing
// the session: the session is not saved.
var errOperationTorn = errors.New("velocity/auth: session not saved: an authentication operation on this request was interrupted by a panic")

// gateOp is one operation holding a request's gate: what it changes in the
// holder is staged here and published when it ends. The zero value with a
// nil holder is an operation on a request without one (the scheme driven
// with no session context), whose steps apply to nothing.
type gateOp struct {
	h *sessionHolder
	// bumps counts the transitions the operation began (see
	// beginTransition); published into h.transition.
	bumps uint64
	// staged holds the credential writes the operation queued, bound to
	// the transition in effect when each was queued.
	staged []afterSaveWrite
	// ended is set once op published or aborted; a later end is a no-op,
	// so an operation may publish as soon as its state is whole and run
	// the rest of its work with the gate free.
	ended bool
}

// reserve takes the gate for op, or returns auth.ErrOperationInProgress
// when another operation holds it. It never waits. A nil holder has no
// gate: op then runs unguarded, as it has nothing to publish.
func (h *sessionHolder) reserve(op *gateOp) error {
	op.h = h
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy {
		return auth.ErrOperationInProgress
	}
	h.busy = true
	return nil
}

// reserveCommit takes the gate for the commit. It seals the request first,
// in the same step, so an operation that holds the gate meanwhile is
// refused when it publishes. When an operation holds the gate, or one was
// torn, the commit is refused: it returns the reason and must save
// nothing.
func (h *sessionHolder) reserveCommit() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sealed = true
	switch {
	case h.busy:
		return auth.ErrOperationInProgress
	case h.torn:
		return errOperationTorn
	}
	h.busy = true
	return nil
}

// releaseGate frees the gate the commit holds.
func (h *sessionHolder) releaseGate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.busy = false
}

// sessionForRead returns the published session for a read of the user, and
// whether the read must fail closed instead: an operation holds the gate
// (busy), or one was torn. One lock, as a read took before the gate.
func (h *sessionHolder) sessionForRead() (s auth.Session, busy, torn bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.session, h.busy, h.torn
}

// beginTransition records that op replaced the request's session state
// (the session was regenerated or invalidated): once op publishes, the
// credential writes earlier transitions queued are superseded, so an
// operation that fails before it changed anything leaves them to be
// delivered, or undone, as before.
func (op *gateOp) beginTransition() {
	op.bumps++
}

// queueCredentialWrite stages e, bound to the transition op is in.
func (op *gateOp) queueCredentialWrite(e afterSaveWrite) {
	if op.h == nil {
		return
	}
	op.h.mu.RLock()
	e.transition = op.h.transition + op.bumps
	op.h.mu.RUnlock()
	op.staged = append(op.staged, e)
}

// publish ends op: it applies op's transitions and queued writes to the
// holder in one step and frees the gate, and reports true. With
// refuseSealed, an operation whose request was sealed meanwhile (the
// response was committed while op ran, so nothing op changed can be saved)
// is refused instead: nothing is applied, the undo steps of the writes op
// queued run once the gate is free, and publish reports false. A published
// transition ends a torn state: the session is whole again.
func (op *gateOp) publish(refuseSealed bool) bool {
	h := op.h
	if h == nil || op.ended {
		return true
	}
	op.ended = true
	h.mu.Lock()
	if refuseSealed && h.sealed {
		h.busy = false
		h.mu.Unlock()
		for _, e := range op.staged {
			if e.undo != nil {
				e.undo()
			}
		}
		return false
	}
	h.transition += op.bumps
	h.afterSave = append(h.afterSave, op.staged...)
	if op.bumps > 0 {
		h.torn = false
	}
	h.busy = false
	h.mu.Unlock()
	return true
}

// abort ends op unwound by a panic: nothing op staged is applied, the gate
// is freed, and when op had begun changing the session the request is
// marked torn, so no scheme read uses the session and the commit does not
// save it. The undo steps of the writes op queued run, each contained, so
// the panic goes on unchanged.
func (op *gateOp) abort() {
	h := op.h
	if h == nil || op.ended {
		return
	}
	op.ended = true
	h.mu.Lock()
	if op.bumps > 0 {
		h.torn = true
	}
	h.busy = false
	h.mu.Unlock()
	for _, e := range op.staged {
		if e.undo != nil {
			runContained(e.undo)
		}
	}
}

// runContained runs fn, dropping a panic from it.
func runContained(fn func()) {
	defer func() { _ = recover() }()
	fn()
}
