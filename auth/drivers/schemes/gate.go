package schemes

import (
	"errors"
	"net/http"
	"reflect"
	"runtime"

	"github.com/velocitykode/velocity/auth"
)

// Every access the scheme makes, for a request, to the request's session
// or to a store on the request's behalf runs under one reservation: the
// gate on the request's sessionHolder. That covers the reads of the
// signed-in user (User, Check, CheckWithError, ID, and the remember-me
// recall or burn a read may run), ResolveSession, Login, LoginByID,
// Attempt, Logout, and the session middleware's commit. The gate never
// makes an operation wait for another: an operation reserves it under the
// holder's short mutex, runs the user code it calls (user store, session
// store, server session store, CSRF rotator, remember-token swaps,
// loggers) with no lock held, and publishes what it changed in the holder
// in one step under the mutex when it ends. An operation that finds the
// gate reserved, a competing goroutine of the request or a store calling
// back into the scheme for the request it serves, fails closed with
// auth.ErrOperationInProgress: no user, Check false, the error wherever
// the method returns one.
//
// A read of the signed-in user is the one exception to "never waits", and
// only for another read. The first read of a request (the resolver)
// reserves the gate, walks the authentication ladder and publishes its
// outcome: the user, whether the request is authenticated and why not,
// together. Later reads return that published identity under one read
// lock, with no user code, until an operation that may change the session
// reserves the gate or an operation is torn, which clears it. A read on
// another goroutine of the request that arrives while the resolver runs
// waits for its publication (or for the end of its request's context) and
// then reads again. A read that would wait from inside a resolve or a wait
// on its own goroutine (a store the resolver calls, or a context whose
// Done calls back in) is re-entry and fails closed at once: waiting would
// wait on itself. It is recognized by the frames on the reader's own
// stack (see onResolvePath), inspected only when a read meets a resolve in
// progress, never on an uncontended read. A cycle through goroutines the
// user code creates (a store that starts a goroutine which reads the user
// and waits for it) is not detected: it ends when the request's context
// does.
//
// The session object itself (auth.Session, which a custom SessionStore
// implements) is changed in place by the operation that holds the gate:
// Regenerate, the user id, Invalidate. What makes that safe is that
// nothing reads or saves it halfway: no scheme read touches it while
// another operation holds the gate, and the commit never saves while an
// operation holds the gate, nor after one was torn (unwound by a panic
// after it began changing the session), until a later sign-in or logout
// of the request publishes a whole state again. A handler reading the raw
// session object concurrently with its own Login is outside this.
//
// A Login or Logout outside the session middleware commits on a holder of
// its own; when the request carries a holder WithSessionContext attached,
// the operation also reserves that holder (its anchor), so reads and
// operations on the request see it in flight. Without either, nothing
// per-request exists to reserve, and a store that calls back into the
// scheme for the same request is not detected.

// errOperationTorn is what the commit returns when an authentication
// operation of the request was unwound by a panic after it began changing
// the session: the session is not saved.
var errOperationTorn = errors.New("velocity/auth: session not saved: an authentication operation on this request was interrupted by a panic")

// resolvedIdentity is the outcome of a read of the signed-in user: the
// user, whether the request is authenticated, and the reason it is not.
type resolvedIdentity struct {
	user auth.Authenticatable
	ok   bool
	err  error
}

// signedOut is the identity of a request an operation was torn on.
var signedOut = resolvedIdentity{}

// resolution is one resolver's turn: done closes when it releases the
// gate, and ident is the identity it published by then (nil when it
// published none: it was ResolveSession, or it was torn). Its waiters
// return ident: their reads overlapped its publication.
type resolution struct {
	done  chan struct{}
	ident *resolvedIdentity
}

// gateOp is one operation holding a request's gate: what it changes in the
// holder is staged here and published when it ends. The zero value with a
// nil holder is an operation on a request without one (the scheme driven
// with no session context), whose steps apply to nothing.
type gateOp struct {
	h *sessionHolder
	// anchor is the request's own holder a standalone operation reserves
	// besides h, the holder it commits on (see reserveOperation).
	anchor *sessionHolder
	// read marks the gate's resolver: a read of the signed-in user (or
	// ResolveSession), which other reads of the request wait for.
	read bool
	// identity, when set, is published with op as the request's
	// identity (see sessionHolder.ident).
	identity *resolvedIdentity
	// bumps counts the transitions the operation began (see
	// beginTransition); published into h.transition.
	bumps uint64
	// staged holds the credential writes the operation queued, bound to
	// the transition in effect when each was queued.
	staged []afterSaveWrite
	// mutated is set before op calls the first thing that can change the
	// request's session (see beginMutation): a panic from then on leaves
	// the session possibly half-changed, so abort marks the request torn.
	mutated bool
	// applied is set once op's changes were applied to the holder (see
	// apply); op still holds the gate until it is released.
	applied bool
	// refused is set when apply refused op's changes (the request was
	// sealed meanwhile).
	refused bool
	// ended is set once op released the gate or aborted; a later end is a
	// no-op.
	ended bool
	// endsSession marks a Logout: publishing marks the holder's session
	// ended (sessionHolder.ended).
	endsSession bool
	// fresh is the session a sign-in started after the holder's session
	// was ended: publishing installs it and clears the mark.
	fresh auth.Session
}

// take reserves the gate for an operation that may change the request's
// session, or returns auth.ErrOperationInProgress when another operation
// holds it. It never waits. The published identity is cleared: it may not
// outlive what the operation changes.
func (h *sessionHolder) take() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.busy {
		return auth.ErrOperationInProgress
	}
	h.busy = true
	h.ident = nil
	return nil
}

// reserve takes the gate for op (see take). A nil holder has no gate: op
// then runs unguarded, as it has nothing to publish. A refused op holds
// nothing, so ending it (publish, release, abort) is a no-op and never
// frees the gate the other operation holds.
func (h *sessionHolder) reserve(op *gateOp) error {
	if h == nil {
		return nil
	}
	if err := h.take(); err != nil {
		return err
	}
	op.h = h
	return nil
}

// reserveOperation takes the request's reservation for an operation that
// may change the session (Login, LoginByID, Attempt, Logout) and returns
// the holder it stages and commits on, and whether that holder is the
// operation's own (standalone: r runs outside the session middleware). A
// standalone operation whose r carries a holder WithSessionContext
// attached reserves that holder first, as its anchor: the request's reads
// and operations see the operation in flight. Refused, it returns
// auth.ErrOperationInProgress before any work.
func reserveOperation(r *http.Request, op *gateOp) (holder *sessionHolder, standalone bool, err error) {
	holder, standalone, anchor := seamHolder(r)
	if anchor != nil {
		if err := anchor.take(); err != nil {
			return nil, false, err
		}
		op.anchor = anchor
	}
	if err := holder.reserve(op); err != nil {
		op.releaseAnchor(false)
		return nil, false, err
	}
	return holder, standalone, nil
}

// readTurn waits for the request's turn to read: it returns the published
// identity when one is published and usePublished is set, or reserves the
// gate for op as the request's resolver and returns nil, or returns the
// error the read fails closed with. A read that finds another goroutine of
// the request resolving waits for it (or for the end of r's context) and
// looks again; one that finds an operation that may change the session in
// flight, or that would wait from inside a resolve or a wait of its own
// goroutine (see onResolvePath), fails closed at once.
func (h *sessionHolder) readTurn(r *http.Request, op *gateOp, usePublished bool) (*resolvedIdentity, error) {
	for {
		h.mu.Lock()
		switch {
		case usePublished && h.torn:
			h.mu.Unlock()
			return &signedOut, nil
		case usePublished && h.ident != nil && !(h.busy && !h.resolving):
			id := h.ident
			h.mu.Unlock()
			return id, nil
		case h.busy && !h.resolving:
			h.mu.Unlock()
			return nil, auth.ErrOperationInProgress
		case h.busy:
			turn := h.resolution
			h.mu.Unlock()
			// The stack is inspected with no lock held: it is the
			// contended path only, and a resolve that ends meanwhile
			// has closed turn.done, so the wait below returns at once.
			if onResolvePath() {
				return nil, auth.ErrOperationInProgress
			}
			if !h.awaitResolver(r, turn.done) {
				return nil, auth.ErrOperationInProgress
			}
			if usePublished && turn.ident != nil {
				return turn.ident, nil
			}
			continue
		}
		h.busy = true
		h.resolving = true
		h.resolution = &resolution{done: make(chan struct{})}
		h.mu.Unlock()
		op.h = h
		op.read = true
		return nil, nil
	}
}

// awaitResolver waits until the resolver closes resolved or r's context
// ends, and reports whether the resolver was first. r's context is user
// code: it is asked for Done with no lock held, and inside this frame, so
// a Done that reads the user on this goroutine finds it on its stack and
// is refused as re-entry (see onResolvePath). waiters counts the reads
// waiting meanwhile.
func (h *sessionHolder) awaitResolver(r *http.Request, resolved <-chan struct{}) (first bool) {
	h.mu.Lock()
	h.waiters++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.waiters--
		h.mu.Unlock()
	}()
	select {
	case <-resolved:
		return true
	default:
	}
	select {
	case <-resolved:
		return true
	case <-r.Context().Done():
		return false
	}
}

// published returns the identity a read of the user returns without
// taking its turn: signed out on a torn request, the published identity,
// or auth.ErrOperationInProgress while an operation that may change the
// session holds the gate. ok is false when the read must take its turn
// (see readTurn). One read lock, no user code.
func (h *sessionHolder) published() (id *resolvedIdentity, err error, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	switch {
	case h.torn:
		return &signedOut, nil, true
	case h.busy && !h.resolving:
		return nil, auth.ErrOperationInProgress, true
	case h.ident != nil:
		return h.ident, nil, true
	}
	return nil, nil, false
}

// reserveCommit takes the gate for the commit. It seals the request first,
// in the same step, so an operation that holds the gate meanwhile is
// refused when it publishes. When an operation holds the gate, or one was
// torn, the commit is refused: it returns the reason and must save
// nothing. The commit may end the session, so the published identity is
// cleared.
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
	h.ident = nil
	return nil
}

// releaseGate frees the gate the commit holds.
func (h *sessionHolder) releaseGate() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.busy = false
}

// beginTransition records that op replaced the request's session state
// (the session was regenerated or invalidated): once op publishes, the
// credential writes earlier transitions queued are superseded, so an
// operation that fails before it changed anything leaves them to be
// delivered, or undone, as before.
func (op *gateOp) beginTransition() {
	op.mutated = true
	op.bumps++
}

// beginMutation records that op is about to call something that can
// change the request's session (Regenerate, Invalidate, Remove, a CSRF
// token rotation or revocation, which change the token the session
// keeps). It is called before that call, since user code can change the
// session and then panic: from here on a panic tears the request (see
// abort).
func (op *gateOp) beginMutation() {
	op.mutated = true
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

// publish ends op: it applies op's changes (see apply) and frees the gate,
// and reports whether the changes were applied.
func (op *gateOp) publish(refuseSealed bool) bool {
	applied := op.apply(refuseSealed)
	op.release()
	return applied
}

// apply applies op's transitions, queued writes and identity to the
// holder in one step and reports true; op keeps the gate until it is
// released, so the work that follows a whole state (a Logout's
// server-side teardown, the commit of an operation outside the session
// middleware) still runs under op's reservation. With refuseSealed, an
// operation whose request was sealed meanwhile (the response was committed
// while op ran, so nothing op changed can be saved) is refused instead:
// nothing else is applied, a read publishes the signed-out identity, the
// undo steps of the writes op queued run, still under op's reservation,
// and apply reports false. A published transition ends a torn state: the
// session is whole again. apply runs once; a later call reports whether
// the first applied anything.
func (op *gateOp) apply(refuseSealed bool) bool {
	h := op.h
	if h == nil {
		return true
	}
	if op.applied || op.ended {
		return !op.refused
	}
	op.applied = true
	h.mu.Lock()
	if refuseSealed && h.sealed {
		if op.read {
			h.ident = &signedOut
			h.resolution.ident = h.ident
		}
		h.mu.Unlock()
		op.refused = true
		for _, e := range op.staged {
			if e.undo != nil {
				e.undo()
			}
		}
		return false
	}
	h.transition += op.bumps
	h.afterSave = append(h.afterSave, op.staged...)
	if op.endsSession {
		h.ended = true
	}
	if op.fresh != nil {
		h.session = op.fresh
		h.ended = false
	}
	if op.bumps > 0 {
		h.torn = false
	}
	if op.identity != nil {
		h.ident = op.identity
		h.resolution.ident = op.identity
	}
	h.mu.Unlock()
	if a := op.anchor; a != nil && op.bumps > 0 {
		a.mu.Lock()
		a.torn = false
		a.mu.Unlock()
	}
	return true
}

// release frees the gate op holds, then its anchor's. A resolver's waiters
// are woken. A later release or abort is a no-op.
func (op *gateOp) release() {
	h := op.h
	if h == nil || op.ended {
		op.releaseAnchor(false)
		return
	}
	op.ended = true
	h.mu.Lock()
	h.freeLocked(op)
	h.mu.Unlock()
	op.releaseAnchor(false)
}

// freeLocked frees the gate op holds; h.mu is held.
func (h *sessionHolder) freeLocked(op *gateOp) {
	h.busy = false
	if op.read {
		h.resolving = false
		close(h.resolution.done)
		h.resolution = nil
	}
}

// releaseAnchor frees the anchor op reserved, marking it torn when torn is
// set. A later call is a no-op.
func (op *gateOp) releaseAnchor(torn bool) {
	a := op.anchor
	if a == nil {
		return
	}
	op.anchor = nil
	a.mu.Lock()
	if torn {
		a.torn = true
	}
	a.busy = false
	a.mu.Unlock()
}

// abort ends op unwound by a panic. Before op applied its changes, nothing
// op staged is applied and, when op had begun changing the session (see
// beginMutation), the request is marked torn (its anchor too), so no scheme
// read uses the session and the commit does not save it; the undo steps of
// the writes op queued run, each contained, so the panic goes on
// unchanged. After op applied its changes the session is whole, so abort
// only frees the gate. Either way the published identity is cleared.
// Deferred by every operation, it is a no-op once op released the gate.
func (op *gateOp) abort() {
	h := op.h
	if h == nil || op.ended {
		op.releaseAnchor(op.mutated && !op.applied)
		return
	}
	if op.applied {
		op.release()
		return
	}
	op.ended = true
	h.mu.Lock()
	if op.mutated {
		h.torn = true
	}
	h.ident = nil
	h.freeLocked(op)
	h.mu.Unlock()
	op.releaseAnchor(op.mutated)
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

// resolveFrames names the functions a goroutine is inside while it holds
// a read's turn (resolveReserved, resolveSessionTurn) or waits for one
// (awaitResolver). They run the user code a read calls: user and session
// stores, remember-token swaps, the request context's Done.
var resolveFrames = [...]string{
	funcName((*SessionScheme).resolveReserved),
	funcName((*SessionScheme).resolveSessionTurn),
	funcName((*sessionHolder).awaitResolver),
}

func funcName(fn any) string {
	return runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
}

// onResolvePath reports whether the calling goroutine is inside a read's
// turn or a wait for one, of any request: a read that meets a resolve in
// progress and would wait for it is then waiting from inside user code a
// resolve or a wait runs, on this goroutine, which cannot return until
// the read does, so it would wait on itself (or, across two requests
// resolving on each other's behalf, on a cycle). Such a read fails closed
// instead. A read on another goroutine carries none of these frames and
// waits.
//
// It scans the whole stack, growing its buffer until the stack fits, so
// the answer is never cut short by depth. It costs a stack walk and runs
// only on that contended path; an uncontended read never calls it.
func onResolvePath() bool {
	var buf [64]uintptr
	pcs := buf[:]
	for {
		n := runtime.Callers(2, pcs)
		if n < len(pcs) {
			pcs = pcs[:n]
			break
		}
		pcs = make([]uintptr, 2*len(pcs))
	}
	frames := runtime.CallersFrames(pcs)
	for {
		f, more := frames.Next()
		for _, name := range resolveFrames {
			if f.Function == name {
				return true
			}
		}
		if !more {
			return false
		}
	}
}
