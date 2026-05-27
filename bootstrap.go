package velocity

import (
	"context"
	"fmt"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
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

	// 2a. Auto-install the save-at-end session middleware BEFORE any
	// consumer middleware so it wraps every request: session writes
	// inside the handler (Put/Flash/login-helpers) must be persisted by
	// the framework, not by every consumer remembering to call Save(w).
	// See guards.SessionGuard.SessionMiddleware for the contract.
	//
	// Installed only when the default auth guard is the session guard;
	// JWT-only or other configurations are skipped (no session bag to
	// persist). Idempotent under repeated bootstrap() calls because
	// bootstrapped=true short-circuits before any middleware wiring.
	installSessionMiddleware(a)

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

	// 8. Refuse to run with CookieStore-only sessions in production
	// unless the operator explicitly opted in. The CookieStore in-process
	// revocation list (H-04) closes the captured-cookie window on a
	// single host, but cannot propagate across a fleet on its own. See
	// validateSessionStoreForProduction for the full contract.
	if err := validateSessionStoreForProduction(a); err != nil {
		return err
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

	// C-03-fb2 HIGH 1: the batch package fires lifecycle events
	// (BatchCreated, BatchJobCompleted, BatchJobFailed, BatchCompleted,
	// BatchCancelled) from inside the repository when ANY host observes
	// a state transition. Previously these dispatches went to the
	// per-batch dispatcher (nil when the dispatcher process did not call
	// WithEventDispatcher, silently dropped). Routing through the
	// app-wide events dispatcher ensures subscribers via events.Listen
	// see the notification regardless of which host fired the CAS.
	queue.SetGlobalEventDispatcher(dispatch)

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

	// Wire events into any extension that supports it. Iterate under the
	// Services extMu RLock via RangeExtensions so a concurrent
	// RegisterExtension cannot race the iteration (cross-cutting map
	// mutex sweep, rule #3).
	a.Services.RangeExtensions(func(_ string, ext any) bool {
		if s, ok := ext.(contract.EventDispatcherAware); ok {
			s.SetEventDispatcher(dispatch)
		}
		return true
	})
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

// ErrCookieStoreInProduction is returned by App.Bootstrap when the app is
// running with APP_ENV unset / production AND the session guard is using
// the default CookieStore AND no ServerSessionStore has been installed AND
// the operator has not opted in via SessionConfig.AllowCookieStoreInProduction.
//
// CookieStore alone cannot enforce a cross-process Logout: the in-process
// revocation list (H-04 fix) only closes the captured-cookie window on the
// host that handled Logout. A multi-host deployment MUST install a real
// ServerSessionStore (Redis/SQL) via Manager.SetServerSessionStore so
// revocations propagate. See audit findings H-04 / 02-session.md for the
// full attack model.
var ErrCookieStoreInProduction = fmt.Errorf("velocity/auth: production deployment must install a ServerSessionStore (or opt-in via SessionConfig.AllowCookieStoreInProduction)")

// validateSessionStoreForProduction implements the H-04 boot-time guard.
// Skip in testing/development; skip when the active guard is not the session
// guard (JWT-only setups carry their own credentials); skip when a
// ServerSessionStore has been installed by an earlier Boot() hook; skip when
// the operator opted in.
//
// The check runs at the END of bootstrap so providers that wire a
// ServerSessionStore in their Boot() callback are honoured before the gate
// fires.
func validateSessionStoreForProduction(a *App) error {
	if a == nil || a.config == nil {
		return nil
	}
	switch a.config.Env {
	case "testing", "development":
		return nil
	}
	if a.config.Session.AllowCookieStoreInProduction {
		return nil
	}
	mgr, ok := a.Auth.(*auth.Manager)
	if !ok {
		return nil
	}
	guard, err := mgr.DefaultGuard()
	if err != nil {
		return nil
	}
	if _, ok := guard.(*guards.SessionGuard); !ok {
		return nil
	}
	if mgr.ServerSessionStore() != nil {
		return nil
	}
	return ErrCookieStoreInProduction
}

// installSessionMiddleware mounts guards.SessionGuard.SessionMiddleware
// onto the router as the outermost global middleware when the active
// default auth guard is a *SessionGuard. The fix for security audit H-05
// (CONFIRMED HIGH: "No save-at-end session middleware installed").
//
// Without this hook, every ctx.Auth().Session(r).Put / Flash call inside
// a handler is silently dropped because the cookie session store is only
// flushed by an explicit Session.Save(w). Laravel's StartSession is the
// reference implementation: handle, then saveSession on the way out.
//
// We type-assert through contract.AuthManager because a.Services.Auth is
// the public interface (the auth/csrf/view packages cannot import each
// other directly without a cycle). When the assertion fails (no auth
// configured, custom manager, JWT-only setup) we skip silently: there is
// no session bag to persist in those modes.
//
// Idempotent: bootstrap() guards against double-install via the
// a.bootstrapped flag.
func installSessionMiddleware(a *App) {
	if a.Auth == nil || a.Router == nil {
		return
	}
	mgr, ok := a.Auth.(*auth.Manager)
	if !ok {
		return
	}
	guard, err := mgr.DefaultGuard()
	if err != nil {
		return
	}
	sg, ok := guard.(*guards.SessionGuard)
	if !ok {
		return
	}
	a.Router.Use(sg.SessionMiddleware())
}
