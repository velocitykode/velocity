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

func Register(fn RegistrationFunc) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registrations = append(registrations, fn)
}

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

func LoadRoutes() {
	applyRegistrations(Get())
}
