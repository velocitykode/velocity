package ownctx

import (
	"context"
	"sync"
	"time"
)

// Held is work that user code does on behalf of a statement run under a
// lock, kept until the lock is released. A database pool the ORM's
// drivers package opened calls its statement observer and query logger
// from inside the database/sql call that runs the statement; given a
// context from Hold or HoldDetached, it hands that work to the context's
// Held instead (Defer), and the component runs it with Release once it
// has unlocked, so the observer and the logger never run under the lock:
//
//	owned, held := ownctx.Hold(ctx)
//	defer held.Release() // registered first: runs after the unlock
//	mu.Lock()
//	defer mu.Unlock()
//	_, err := db.ExecContext(owned, query)
//
// Work deferred after Release runs where it is deferred: a result set
// database/sql closes later, from its own goroutine, reports its statement
// then, and no lock of the component is held there. A Held is safe for
// concurrent use.
type Held struct {
	mu       sync.Mutex
	pending  []func()
	released bool
}

// Hold is Bridge, returning a context that also holds the statement work
// deferred under it until Release. Call it before taking the lock, as
// Bridge.
func Hold(ctx context.Context) (context.Context, *Held) {
	h := &Held{}
	if ctx == nil {
		return &owned{held: h}, h
	}
	done := ctx.Done()
	deadline, hasDeadline := ctx.Deadline()
	return &owned{done: done, deadline: deadline, hasDeadline: hasDeadline, ids: readIDs(ctx), held: h}, h
}

// HoldDetached is Detached bounded by timeout, returning a context that
// also holds the statement work deferred under it until Release, and the
// cancel that ends it early (call it, as context.WithTimeout's). Call it
// before taking the lock, as Detached. The bound is the context's own,
// not a context derived from it: HeldBy answers only a context built
// here.
func HoldDetached(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc, *Held) {
	h := &Held{}
	bound, cancel := context.WithTimeout(context.Background(), timeout)
	deadline, _ := bound.Deadline()
	o := &owned{done: bound.Done(), deadline: deadline, hasDeadline: true, held: h}
	if ctx != nil {
		o.ids = readIDs(ctx)
	}
	return o, cancel, h
}

// HeldBy returns the Held of a context built by Hold or HoldDetached, nil
// for any other context, a context derived from one included. It runs no
// method of ctx, so it is safe on a caller's context, under a lock or not,
// and costs a type assertion.
func HeldBy(ctx context.Context) *Held {
	if o, ok := ctx.(*owned); ok {
		return o.held
	}
	return nil
}

// Defer keeps fn to run at Release and reports true, or reports false
// when h is nil or already released, so the caller runs fn itself.
func (h *Held) Defer(fn func()) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.released {
		return false
	}
	h.pending = append(h.pending, fn)
	return true
}

// Release runs the work deferred so far, in the order it was deferred, on
// the calling goroutine, and makes later Defer calls report false. Call it
// after unlocking. The work is the framework's own contained code (see
// Held), so a panic in it is not recovered here. A second Release, and a
// Release of a nil Held, does nothing.
func (h *Held) Release() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.released {
		h.mu.Unlock()
		return
	}
	h.released = true
	pending := h.pending
	h.pending = nil
	h.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}
