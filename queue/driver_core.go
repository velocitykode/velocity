package queue

import (
	"context"

	"github.com/velocitykode/velocity/internal/eventemit"
)

// DriverCore is the shared event-dispatch slot embedded by every built-in
// queue driver: the in-package memory and database drivers and the
// out-of-package redis leaf. Its emitter holds the dispatcher behind an
// atomic pointer, so SetEventDispatcher and DispatchEvent never acquire a
// lock, which is what lets the push paths dispatch while still holding the
// driver's mutex without self-deadlocking. Embedders satisfy
// contract.EventDispatcherAware through the promoted SetEventDispatcher.
type DriverCore struct {
	// events holds the dispatcher and handles a failed dispatch. It takes
	// no lock, because the drivers' push paths call DispatchEvent while
	// holding their own mutex.
	events eventemit.Emitter
}

// SetEventDispatcher installs the event dispatcher. The assignment is atomic
// and never touches a lock, so it is safe to call from inside callers that
// already hold the driver lock. A nil fn clears the dispatcher.
func (c *DriverCore) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	c.events.Set(fn)
}

// DispatchFunc returns DispatchEvent when an event dispatcher is installed
// and nil when none is. The lifecycle event helpers (DispatchJobQueued,
// DispatchJobFailed) build no event for a nil function, so a driver hands
// them DispatchFunc() and builds no event for no listener.
func (c *DriverCore) DispatchFunc() func(ctx context.Context, event interface{}) {
	if !c.events.Installed() {
		return nil
	}
	return c.DispatchEvent
}

// DispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped values;
// a nil ctx falls back to context.Background. The dispatcher is loaded
// atomically, so this is safe to invoke from paths that already hold a driver
// lock. A failed dispatch is counted and its event's first failure logged
// through the framework's standalone fallback logger (see
// internal/eventemit); in an app the dispatch function the framework hands
// the driver has already recorded it through the app logger.
func (c *DriverCore) DispatchEvent(ctx context.Context, event interface{}) {
	c.events.Emit(ctx, event)
}
