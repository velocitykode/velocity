package app

import "context"

// Module defines the lifecycle contract for modular service registration.
// Modules are called in two phases: Init (bind services) then Start (wire them).
// Shutdown is called in reverse registration order during application teardown.
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
