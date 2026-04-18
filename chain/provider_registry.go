// Package chain provides the declarative bootstrap types consumer apps use
// inside their Providers / Middleware / Routes / Commands callbacks.
//
// These types live here (not in the root velocity package) so user code can
// import a narrow, leaf-like package instead of pulling the whole framework
// into every callback signature.
package chain

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/scheduler"
)

// ProviderRegistry collects service providers during the Providers callback.
type ProviderRegistry struct {
	providers []app.ServiceProvider
}

// Add appends one or more service providers to the registry.
func (r *ProviderRegistry) Add(providers ...app.ServiceProvider) {
	r.providers = append(r.providers, providers...)
}

// Providers returns the collected providers in registration order.
// The returned slice is a copy safe for the caller to iterate or store.
func (r *ProviderRegistry) Providers() []app.ServiceProvider {
	out := make([]app.ServiceProvider, len(r.providers))
	copy(out, r.providers)
	return out
}

// RouteProvider is an optional interface that service providers can implement
// to register routes during bootstrap.
type RouteProvider interface {
	Routes(r *Routing)
}

// MiddlewareProvider is an optional interface that service providers can implement
// to register middleware during bootstrap.
type MiddlewareProvider interface {
	Middleware(m *MiddlewareStack)
}

// EventProvider is an optional interface that service providers can implement
// to register event listeners during bootstrap.
type EventProvider interface {
	Events(d events.Dispatcher)
}

// ScheduleProvider is an optional interface that service providers can implement
// to register scheduled jobs during bootstrap.
type ScheduleProvider interface {
	Schedule(s scheduler.TaskScheduler)
}

// CommandProvider is an optional interface that service providers can implement
// to register custom commands during bootstrap.
type CommandProvider interface {
	Commands(r *Commands)
}
