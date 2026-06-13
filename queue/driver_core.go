package queue

import (
	"context"
	"sync/atomic"
)

// dispatcherFn is the typed function pointer used for atomic.Pointer storage.
// Defined at package scope so all drivers share the same underlying type for
// the dispatcher slot.
type dispatcherFn func(ctx context.Context, event interface{}) error

// DriverCore is the shared event-dispatch slot embedded by every built-in
// queue driver: the in-package memory and database drivers and the
// out-of-package redis leaf. It holds the dispatcher behind an atomic.Pointer
// so SetEventDispatcher and DispatchEvent never acquire a lock, which is what
// lets the push paths dispatch while still holding the driver's mutex without
// self-deadlocking. Embedders satisfy contract.EventDispatcherAware through the
// promoted SetEventDispatcher.
type DriverCore struct {
	// eventDispatcher is stored via atomic.Pointer so the dispatch path never
	// acquires a lock. The drivers' push paths call DispatchEvent while holding
	// their own mutex, so any locking dispatcher path would self-deadlock.
	eventDispatcher atomic.Pointer[dispatcherFn]
}

// SetEventDispatcher installs the event dispatcher. The assignment goes through
// atomic.Pointer and never touches a lock, so it is safe to call from inside
// callers that already hold the driver lock. A nil fn clears the dispatcher.
func (c *DriverCore) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	if fn == nil {
		c.eventDispatcher.Store(nil)
		return
	}
	f := dispatcherFn(fn)
	c.eventDispatcher.Store(&f)
}

// DispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped values;
// a nil ctx falls back to context.Background. The dispatcher pointer is loaded
// atomically, so this is safe to invoke from paths that already hold a driver
// lock.
func (c *DriverCore) DispatchEvent(ctx context.Context, event interface{}) {
	p := c.eventDispatcher.Load()
	if p == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	(*p)(ctx, event)
}
