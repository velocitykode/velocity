package chain

import (
	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/scheduler"
)

// ModuleRegistry collects modules during the Modules callback.
type ModuleRegistry struct {
	modules []app.Module
}

// Add appends one or more modules to the registry.
func (r *ModuleRegistry) Add(modules ...app.Module) {
	r.modules = append(r.modules, modules...)
}

// Modules returns the collected modules in registration order.
// The returned slice is a copy safe for the caller to iterate or store.
func (r *ModuleRegistry) Modules() []app.Module {
	out := make([]app.Module, len(r.modules))
	copy(out, r.modules)
	return out
}

// RouteModule is an optional interface that modules can implement
// to register routes during bootstrap.
type RouteModule interface {
	Routes(r *Routing)
}

// MiddlewareModule is an optional interface that modules can implement
// to register middleware during bootstrap.
type MiddlewareModule interface {
	Middleware(m *MiddlewareStack)
}

// EventModule is an optional interface that modules can implement
// to register event listeners during bootstrap.
type EventModule interface {
	Events(d events.Dispatcher)
}

// ScheduleModule is an optional interface that modules can implement
// to register scheduled jobs during bootstrap.
type ScheduleModule interface {
	Schedule(s scheduler.TaskScheduler)
}

// CommandModule is an optional interface that modules can implement
// to register custom commands during bootstrap.
type CommandModule interface {
	Commands(r *Commands)
}
