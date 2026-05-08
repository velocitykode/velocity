package velocity

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
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
		reg := &chain.ProviderRegistry{}
		a.providersFn(reg)
		a.chainProviders = reg.Providers()
	}

	if err := runProviderLifecycle(a.chainProviders, a.Services, "chain provider"); err != nil {
		return err
	}

	// 2. Build middleware stack
	mwStack := chain.NewMiddlewareStack(a.Services)

	dispatchProviderCallback(a.chainProviders, func(mp chain.MiddlewareProvider) {
		mp.Middleware(mwStack)
	})
	if a.middlewareFn != nil {
		a.middlewareFn(mwStack)
	}
	if global := mwStack.GlobalMiddleware(); len(global) > 0 {
		a.Router.Use(global...)
	}

	// 3. Register routes
	routing := chain.NewRouting(a.Router, mwStack)

	dispatchProviderCallback(a.chainProviders, func(rp chain.RouteProvider) {
		rp.Routes(routing)
	})
	if a.routesFn != nil {
		a.routesFn(routing)
	}

	// 4. Register events
	dispatchProviderCallback(a.chainProviders, func(ep chain.EventProvider) {
		ep.Events(a.Services.Events)
	})
	if a.eventsFn != nil {
		a.eventsFn(a.Services.Events)
	}

	// 5. Register scheduled jobs
	dispatchProviderCallback(a.chainProviders, func(sp chain.ScheduleProvider) {
		sp.Schedule(a.Services.Scheduler)
	})
	if a.scheduleFn != nil {
		a.scheduleFn(a.Services.Scheduler)
	}

	// 6. Register custom commands
	a.commands = chain.NewCommands()
	dispatchProviderCallback(a.chainProviders, func(cp chain.CommandProvider) {
		cp.Commands(a.commands)
	})
	if a.commandsFn != nil {
		a.commandsFn(a.commands)
	}

	// 7. Configure exceptions
	if a.exceptionsFn != nil {
		a.exceptionsFn(a.Services.Exceptions)
	}

	return nil
}

// wireInstanceEvents wires the event dispatcher into every subsystem that
// implements contract.EventDispatcherAware. Each service that fires events
// gets the dispatcher set on its instance; subsystems that don't implement
// the contract are skipped silently (e.g. when a feature is disabled).
func wireInstanceEvents(a *App) {
	if a.Services.Events == nil {
		return
	}

	dispatch := func(ctx context.Context, event any) error {
		if ctx == nil {
			ctx = context.Background()
		}
		return a.Services.Events.Dispatch(ctx, event)
	}

	a.Router.SetEventDispatcher(dispatch)

	candidates := []any{a.DB, a.Cache, a.Notification, a.View, a.Mail, a.Queue, a.Scheduler, a.Auth, a.Crypto}
	for _, svc := range candidates {
		if svc == nil {
			continue
		}
		if s, ok := svc.(contract.EventDispatcherAware); ok {
			s.SetEventDispatcher(dispatch)
		}
	}

	// Wire the kind-aware bus into orm so per-transaction buffered
	// events can route DispatchAsync / DispatchAfter / Until back through
	// the matching dispatcher method instead of collapsing onto Dispatch.
	if mgr, ok := a.DB.(*orm.Manager); ok {
		mgr.SetTxEventBus(a.Services.Events)
	}

	// Wire events into any extension that supports it.
	for _, ext := range a.Extensions {
		if s, ok := ext.(contract.EventDispatcherAware); ok {
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
// interface T (e.g., chain.RouteProvider, chain.EventProvider). This lets providers
// opt into lifecycle hooks without requiring every provider to implement every
// interface.
func dispatchProviderCallback[T any](providers []app.ServiceProvider, fn func(T)) {
	for _, p := range providers {
		if t, ok := any(p).(T); ok {
			fn(t)
		}
	}
}
