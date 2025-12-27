package router

import (
	"sync"
)

// RegistrationFunc is a function that registers routes
type RegistrationFunc func(Router)

var (
	registrations []RegistrationFunc
	registryMu    sync.Mutex
)

// Register adds a route registration function to be called during initialization
func Register(fn RegistrationFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registrations = append(registrations, fn)
}

// RegisterWithPrefix adds a route registration function with a prefix
func RegisterWithPrefix(prefix string, fn RegistrationFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()

	registrations = append(registrations, func(r Router) {
		group := r.Group(prefix)
		fn(group)
	})
}

// applyRegistrations applies all registered routes to the given router
func applyRegistrations(r Router) {
	registryMu.Lock()
	defer registryMu.Unlock()

	for _, fn := range registrations {
		fn(r)
	}
}

// LoadRoutes applies all registered routes to the global router
func LoadRoutes() {
	applyRegistrations(Get())
}
