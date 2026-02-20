package velocity

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/scheduler"
)

// ServiceProvider is a convenience alias for app.ServiceProvider.
type ServiceProvider = app.ServiceProvider

// Services is a convenience alias for app.Services.
type Services = app.Services

// ProviderRegistry collects service providers during the Providers callback.
type ProviderRegistry struct {
	providers []app.ServiceProvider
}

// Add appends one or more service providers to the registry.
func (r *ProviderRegistry) Add(providers ...app.ServiceProvider) {
	r.providers = append(r.providers, providers...)
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
	Schedule(s *scheduler.Scheduler)
}

