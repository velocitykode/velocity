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
// times, but only the first call does the work. The result is sticky: after a
// successful run subsequent calls return nil, and after a failed run they
// return the same error (a partially-completed bootstrap is never re-run,
// because providers, middleware and routes registered before the failure
// would be registered twice).
func (a *App) Bootstrap() error {
	return a.bootstrap()
}

func (a *App) bootstrap() error {
	if a.bootstrapped {
		return a.bootstrapErr
	}
	a.bootstrapped = true
	a.bootstrapErr = a.runBootstrap()
	return a.bootstrapErr
}

func (a *App) runBootstrap() error {
	// 1. Collect and run chain providers
	if a.providersFn != nil {
		reg := &chain.ProviderRegistry{}
		a.providersFn(reg)
		a.chainProviders = reg.Providers()
	}

	if registered, err := runProviderLifecycle(a.chainProviders, a.Services, "chain provider"); err != nil {
		// Unwind providers whose Register completed, in reverse order,
		// mirroring the New() failure path: a direct Bootstrap() caller
		// gets a full provider teardown without having to call Shutdown.
		// The provider that failed Register and any after it are excluded
		// (a failing Register must release anything it opened before
		// returning; later providers never ran at all). Empty the slice
		// afterwards so the Shutdown that follows a failed bootstrap
		// (serveHTTP error path) does not tear the same providers down a
		// second time.
		for i := registered - 1; i >= 0; i-- {
			_ = a.chainProviders[i].Shutdown(context.Background())
		}
		a.chainProviders = nil
		return err
	}

	// Chain providers may have registered registry components or replaced
	// service instances (e.g. s.CSRF) during Register/Boot; the wireInstanceEvents
	// sweep in New() ran before any of them existed, so re-sweep services
	// and components so the final instances receive the dispatcher. Every
	// wiring setter is an idempotent overwrite, so re-running is safe.
	wireInstanceEvents(a)

	// 1a. Re-install the CSRF token rotator on the auth manager NOW,
	// AFTER every chain provider's Boot() has had a chance to replace
	// s.CSRF with a customised instance. New() already wired the
	// rotator at construction time so direct-New consumers (no
	// Bootstrap, no Serve) still get session lifecycle rotation; this
	// second call lets a Boot-phase swap of s.CSRF win. The helper is
	// idempotent (sets a mutex-protected function pointer on
	// auth.Manager); double-install is safe, the last call wins.
	//
	// Without this re-install, a consumer Boot that replaces s.CSRF
	// would leave the auth manager rotating a store no longer in the
	// request path -> Login/Logout rotations silently target a dead
	// store and the first POST after login 419s. See app.go for the
	// matching install at the New-time site.
	installCSRFTokenRotator(a)

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

	// 4. Register events. Under WithoutEvents the dispatcher is nil
	// (New skips creating it), and invoking the registration callbacks
	// with a nil dispatcher would panic on first use inside consumer
	// code, so skip them entirely and warn when any were registered.
	if a.Services.Events != nil {
		dispatchProviderCallback(a.chainProviders, func(ep chain.EventProvider) {
			ep.Events(a.Services.Events)
		})
		if a.eventsFn != nil {
			a.eventsFn(a.Services.Events)
		}
	} else {
		hasProviderEvents := false
		for _, p := range a.chainProviders {
			if _, ok := p.(chain.EventProvider); ok {
				hasProviderEvents = true
				break
			}
		}
		if a.eventsFn != nil || hasProviderEvents {
			a.Log.Warn("events are disabled via WithoutEvents; skipping event listener registration callbacks")
		}
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
	dispatch := buildEventDispatch(a)
	if dispatch == nil {
		return
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

	for _, svc := range eventWiringCandidates(a) {
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

	wireComponentEvents(a)
}

// eventWiringCandidates returns every service instance the wireInstanceEvents
// sweep offers the dispatcher to. Each entry that implements
// contract.EventDispatcherAware gets the dispatcher set; nil entries and
// non-aware instances are skipped by the caller.
//
// Every Services field whose value can implement the contract MUST appear
// here (the Router is wired directly in wireInstanceEvents because it lives
// on App, not Services; Services.Events IS the dispatcher). The conformance
// test in wiring_conformance_test.go sweeps app.Services by reflection and
// fails when an aware field is missing from this slice.
func eventWiringCandidates(a *App) []any {
	return []any{a.DB, a.Cache, a.Notification, a.View, a.Mail, a.Queue, a.Scheduler, a.Auth, a.Crypto, a.CSRF}
}

// buildEventDispatch returns the canonical dispatch closure wrapping
// a.Services.Events with nil-ctx defaulting, or nil when events are
// disabled (WithoutEvents) so callers can skip wiring entirely.
func buildEventDispatch(a *App) func(ctx context.Context, event any) error {
	if a.Services.Events == nil {
		return nil
	}
	return func(ctx context.Context, event any) error {
		if ctx == nil {
			ctx = context.Background()
		}
		return a.Services.Events.Dispatch(ctx, event)
	}
}

// wireComponentEvents wires the event dispatcher into every registry entry
// (registered value plus its hook adapters) that implements
// contract.EventDispatcherAware. It is called from wireInstanceEvents so it
// re-runs after each provider lifecycle (WithProviders in New, chain providers
// in bootstrap): providers
// register components only during Register/Boot, so the New-time sweep would
// always see an empty registry. SetEventDispatcher overwrite is idempotent on
// every conforming type, so re-sweeping already wired components is safe.
//
// Iteration uses RangeComponents, which snapshots under the Services compMu
// RLock so a concurrent Register cannot race the sweep (rule #3).
//
// The value and the hooks are wired independently with no hook == value
// dedupe: interface equality on an uncomparable dynamic type panics, so an
// integrator who passes the value itself as a hook would crash a naive guard.
// Instead the setter is simply called twice in that case, which is safe
// because SetEventDispatcher implementations are required to be synchronized
// (CLAUDE.md security rule #3); the last write wins and there is no race.
func wireComponentEvents(a *App) {
	dispatch := buildEventDispatch(a)
	if dispatch == nil {
		return
	}
	a.Services.RangeComponents(func(_ app.ComponentKey, v any, hooks []any) bool {
		if s, ok := v.(contract.EventDispatcherAware); ok {
			s.SetEventDispatcher(dispatch)
		}
		for _, h := range hooks {
			if s, ok := h.(contract.EventDispatcherAware); ok {
				s.SetEventDispatcher(dispatch)
			}
		}
		return true
	})
}

// runProviderLifecycle executes the two-phase provider startup: all Register() calls
// run first (bind services, no cross-provider usage), then all Boot() calls (wire
// dependencies, all services available). This ordering guarantees that Boot() can
// safely reference services registered by other providers.
//
// The returned count is the number of providers whose Register COMPLETED, so
// failure-path unwinds can scope Shutdown to providers[:registered]. A provider
// whose own Register returns an error is NOT counted (it must release anything
// it opened before returning), and providers after it never ran at all. On a
// Boot failure every Register already completed, so the count is len(providers).
func runProviderLifecycle(providers []app.ServiceProvider, services *app.Services, label string) (int, error) {
	for i, p := range providers {
		if err := p.Register(services); err != nil {
			return i, fmt.Errorf("velocity: %s register failed: %w", label, err)
		}
	}
	for _, p := range providers {
		if err := p.Boot(services); err != nil {
			return len(providers), fmt.Errorf("velocity: %s boot failed: %w", label, err)
		}
	}
	return len(providers), nil
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
	// Route through the canonical helper so "dev", "test", "local"
	// behave the same way as "development" / "testing": all dev/test
	// profiles skip the production-only gate.
	if contract.IsDevOrTestEnv(a.config.Env) {
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

// installCSRFTokenRotator wires the final s.CSRF instance (post chain
// provider Boot) into the auth manager as a contract.CSRFTokenRotator so
// SessionGuard.Login regenerates the per-session CSRF token alongside the
// session id, SessionGuard.Logout revokes it before the session is
// invalidated, and the remember-cookie revival path rotates it across the
// recall regenerate. See contract.CSRFTokenRotator for the full contract.
//
// Boot-order rationale: a chain provider may legitimately replace s.CSRF
// in its Boot() (custom store, different mode, decorator wrapping the
// framework-built instance). Running this install BEFORE Boot would
// freeze the rotator to the original framework-built CSRF, and any
// subsequent consumer swap would leave the auth manager rotating a
// store no longer in the request path -> orphan tokens, first-POST 419.
// So this install runs AFTER runProviderLifecycle returns.
//
// No-op when:
//   - a.CSRF does not implement contract.CSRFTokenRotator (custom
//     CSRFProtector not derived from *csrf.CSRF), or
//   - a.Auth does not expose SetCSRFTokenRotator (custom AuthManager,
//     test fakes that satisfy only contract.AuthManager).
//
// Idempotent: bootstrap() guards against double-run via a.bootstrapped.
func installCSRFTokenRotator(a *App) {
	if a == nil || a.Services == nil {
		return
	}
	rotator, ok := a.CSRF.(contract.CSRFTokenRotator)
	if !ok {
		return
	}
	authMgr, ok := a.Auth.(interface {
		SetCSRFTokenRotator(contract.CSRFTokenRotator)
	})
	if !ok {
		return
	}
	authMgr.SetCSRFTokenRotator(rotator)
}
