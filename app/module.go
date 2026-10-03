package app

import "context"

// Module defines the lifecycle contract for modular service registration.
// Modules are called in two phases: Init (bind services) then Start (wire them).
// Shutdown is called in reverse registration order during application teardown.
//
// Replacing a service: Init and Start may assign any service field of s (a
// pointer value; a value Go cannot compare with == fails New). Each field
// owns the instance it holds: at the next lifecycle boundary the
// framework's own collaborators (the ORM default, the session store, the
// login throttler, the scheduler Locker, the notification channels, ...)
// move to the replacement and the instance it displaced is shut down once,
// unless another field, a registered component or an Unwrap chain still
// holds it. A replacement assigned and replaced again before the next
// boundary is the module's to close, and one put back (A, B, A) closes
// nothing. A replacement that decorates the instance it displaces exposes
// it through an Unwrap method returning the field's type, so the inner
// instance is kept. Unwrap is a pure accessor that returns at once: the
// framework follows it to a bounded depth, stops at a cycle and contains a
// panic, but does not bound its duration, so an Unwrap that blocks holds
// the boundary that calls it. Services are published once bootstrap
// finishes: a field replaced after it is neither re-bound nor shut down.
// Configuring a service is not replacing it: a module configures the
// concrete manager it was given with s.Auth.(*auth.Manager) at Start.
type Module interface {
	// Init binds services into the container. Called before any Start method.
	Init(s *Services) error

	// Start is called after all modules have been initialized.
	// Use this to resolve cross-module dependencies.
	Start(s *Services) error

	// Shutdown gracefully tears down module resources.
	// Called in reverse registration order.
	//
	// Ownership rule: a module that registers a value into the component
	// registry (Register/RegisterFor) MUST NOT also close that value here.
	// The registry sweep in App.Shutdown owns teardown of registered values
	// and runs immediately after module Shutdown; closing it in both places
	// is a double-close.
	Shutdown(ctx context.Context) error
}
