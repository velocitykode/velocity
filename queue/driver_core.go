package queue

import (
	"context"
	"time"

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
	// no lock, because the drivers' push paths call DispatchJobQueued while
	// holding their own mutex.
	events eventemit.Emitter
}

// SetEventDispatcher installs the event dispatcher. The assignment is atomic
// and never touches a lock, so it is safe to call from inside callers that
// already hold the driver lock. A nil fn clears the dispatcher.
func (c *DriverCore) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	c.events.Set(fn)
}

// DispatchJobQueued dispatches a JobQueued lifecycle event for a job
// pushed onto queue under ctx (context.Background when nil). The event is
// built only when a dispatcher is installed. The dispatcher is loaded
// atomically, so this is safe to invoke from paths that already hold a
// driver lock. A failed dispatch is counted and its event's first failure
// logged through the framework's standalone fallback logger (see
// internal/eventemit); in an app the dispatch function the framework hands
// the driver has already recorded it through the app logger.
func (c *DriverCore) DispatchJobQueued(ctx context.Context, jobType, queue string, delayed bool, delay time.Duration) {
	dispatchJobQueued(&c.events, ctx, jobType, queue, delayed, delay)
}

// DispatchJobFailed dispatches a JobFailed lifecycle event, as
// DispatchJobQueued does. The event carries no job id: a driver calls it
// for a payload it could not hydrate into a job.
func (c *DriverCore) DispatchJobFailed(ctx context.Context, jobType, queue string, err error, duration time.Duration) {
	dispatchJobFailed(&c.events, ctx, jobType, queue, "", err, duration)
}

// DispatchBuilt hands the event build returns to the installed dispatcher
// under ctx (context.Background when nil). build runs only when a
// dispatcher is installed, so a driver builds no event, and no field of
// one, for no listener. It reports whether a dispatcher was installed, not
// whether a listener received the event. It is the one path an embedding
// driver has to its dispatcher for an event of its own; a failed dispatch
// goes to the same failure policy as DispatchJobQueued's.
func (c *DriverCore) DispatchBuilt(ctx context.Context, build func() any) bool {
	return c.events.EmitBuilt(ctx, build)
}
