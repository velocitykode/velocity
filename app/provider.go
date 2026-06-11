package app

import "context"

// ServiceProvider defines the lifecycle contract for modular service registration.
// Providers are called in two phases: Register (bind services) then Boot (wire them).
// Shutdown is called in reverse registration order during application teardown.
type ServiceProvider interface {
	// Register binds services into the container. Called before any Boot method.
	Register(s *Services) error

	// Boot is called after all providers have been registered.
	// Use this to resolve cross-provider dependencies.
	Boot(s *Services) error

	// Shutdown gracefully tears down provider resources.
	// Called in reverse registration order.
	//
	// Ownership rule: a provider that registers a value into the component
	// registry (Register/RegisterFor) MUST NOT also close that value here.
	// The registry sweep in App.Shutdown owns teardown of registered values
	// and runs immediately after provider Shutdown; closing it in both places
	// is a double-close.
	Shutdown(ctx context.Context) error
}
