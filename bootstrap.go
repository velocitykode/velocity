package velocity

import (
	"fmt"

	"github.com/velocitykode/velocity/app"
)

// Bootstrap runs the declarative chain (providers, middleware, routes, events,
// schedule, exceptions) without starting the HTTP server. Safe to call multiple
// times — subsequent calls are no-ops.
func (a *App) Bootstrap() error {
	return a.bootstrap()
}

func (a *App) bootstrap() error {
	if a.bootstrapped {
		return nil
	}
	a.bootstrapped = true

	// 1. Collect and run chain providers
	if a.providersFn != nil {
		reg := &ProviderRegistry{}
		a.providersFn(reg)
		a.chainProviders = reg.providers
	}

	if err := runProviderLifecycle(a.chainProviders, a.Services, "chain provider"); err != nil {
		return err
	}

	// 2. Build middleware stack
	mwStack := &MiddlewareStack{services: a.Services}

	dispatchProviderCallback(a.chainProviders, func(mp MiddlewareProvider) {
		mp.Middleware(mwStack)
	})
	if a.middlewareFn != nil {
		a.middlewareFn(mwStack)
	}
	if len(mwStack.global) > 0 {
		a.Router.Use(mwStack.global...)
	}

	// 3. Register routes
	routing := &Routing{router: a.Router, middleware: mwStack}

	dispatchProviderCallback(a.chainProviders, func(rp RouteProvider) {
		rp.Routes(routing)
	})
	if a.routesFn != nil {
		a.routesFn(routing)
	}

	// 4. Register events
	dispatchProviderCallback(a.chainProviders, func(ep EventProvider) {
		ep.Events(a.Services.Events)
	})
	if a.eventsFn != nil {
		a.eventsFn(a.Services.Events)
	}

	// 5. Register scheduled jobs
	dispatchProviderCallback(a.chainProviders, func(sp ScheduleProvider) {
		sp.Schedule(a.Services.Scheduler)
	})
	if a.scheduleFn != nil {
		a.scheduleFn(a.Services.Scheduler)
	}

	// 6. Configure exceptions
	if a.exceptionsFn != nil {
		a.exceptionsFn(a.Services.Exceptions)
	}

	return nil
}

// wireInstanceEvents wires the event dispatcher into service instances.
// Each service that fires events gets the dispatcher set on its instance.
func wireInstanceEvents(a *App) {
	if a.Services.Events == nil {
		return
	}

	dispatch := func(event interface{}) error {
		return a.Services.Events.Dispatch(event)
	}

	a.Router.SetEventDispatcher(dispatch)

	if a.DB != nil {
		a.DB.SetEventDispatcher(dispatch)
	}
	if a.Cache != nil {
		a.Cache.SetEventDispatcher(dispatch)
	}
	if a.Notification != nil {
		a.Notification.SetEventDispatcher(dispatch)
	}
	if v, ok := a.View.(interface {
		SetEventDispatcher(func(event interface{}) error)
	}); ok && v != nil {
		v.SetEventDispatcher(dispatch)
	}

	// Wire events into any extension that supports it.
	type eventDispatcherSetter interface {
		SetEventDispatcher(func(event interface{}) error)
	}
	for _, ext := range a.Extensions {
		if s, ok := ext.(eventDispatcherSetter); ok {
			s.SetEventDispatcher(dispatch)
		}
	}
}

// runProviderLifecycle executes the two-phase provider startup: all Register() calls
// run first (bind services, no cross-provider usage), then all Boot() calls (wire
// dependencies, all services available). This ordering guarantees that Boot() can
// safely reference services registered by other providers.
func runProviderLifecycle(providers []app.ServiceProvider, services *app.Services, label string) error {
	for _, p := range providers {
		if err := p.Register(services); err != nil {
			return fmt.Errorf("velocity: %s register failed: %w", label, err)
		}
	}
	for _, p := range providers {
		if err := p.Boot(services); err != nil {
			return fmt.Errorf("velocity: %s boot failed: %w", label, err)
		}
	}
	return nil
}

// dispatchProviderCallback invokes fn on each provider that implements the optional
// interface T (e.g., RouteProvider, EventProvider). This lets providers opt into
// lifecycle hooks without requiring every provider to implement every interface.
func dispatchProviderCallback[T any](providers []app.ServiceProvider, fn func(T)) {
	for _, p := range providers {
		if t, ok := any(p).(T); ok {
			fn(t)
		}
	}
}
