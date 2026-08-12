package velocity

import (
	"context"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/trace"
)

// Bootstrap runs the declarative chain (modules, middleware, routes, events,
// schedule, exceptions) without starting the HTTP server. Safe to call multiple
// times, but only the first call does the work. The result is sticky: after a
// successful run subsequent calls return nil, and after a failed run they
// return the same error (a partially-completed bootstrap is never re-run,
// because modules, middleware and routes registered before the failure
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
	// 1. Collect and run chain modules
	if a.modulesFn != nil {
		reg := &chain.ModuleRegistry{}
		a.modulesFn(reg)
		a.chainModules = reg.Modules()
	}

	if registered, err := runModuleLifecycle(a.chainModules, a.Services, "chain module"); err != nil {
		// Unwind modules whose Init completed, in reverse order,
		// mirroring the New() failure path: a direct Bootstrap() caller
		// gets a full module teardown without having to call Shutdown.
		// The module that failed Init and any after it are excluded
		// (a failing Init must release anything it opened before
		// returning; later modules never ran at all). Empty the slice
		// afterwards so the Shutdown that follows a failed bootstrap
		// (serveHTTP error path) does not tear the same modules down a
		// second time.
		for i := registered - 1; i >= 0; i-- {
			_ = a.chainModules[i].Shutdown(context.Background())
		}
		a.chainModules = nil
		return err
	}

	// Chain modules may have registered registry components or replaced
	// service instances (e.g. s.CSRF) during Init/Start; the wireInstanceEvents
	// sweep in New() ran before any of them existed, so re-sweep services
	// and components so the final instances receive the dispatcher. Every
	// wiring setter is an idempotent overwrite, so re-running is safe.
	wireInstanceEvents(a)

	// 1a. Re-install the CSRF token rotator on the auth manager NOW,
	// AFTER every chain module's Start() has had a chance to replace
	// s.CSRF with a customised instance. New() already wired the
	// rotator at construction time so direct-New consumers (no
	// Bootstrap, no Serve) still get session lifecycle rotation; this
	// second call lets a Start-phase swap of s.CSRF win. The helper is
	// idempotent (sets a mutex-protected function pointer on
	// auth.Manager); double-install is safe, the last call wins.
	//
	// Without this re-install, a consumer Start that replaces s.CSRF
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
	// See schemes.SessionScheme.SessionMiddleware for the contract.
	//
	// Installed only when the default auth scheme is the session scheme;
	// JWT-only or other configurations are skipped (no session bag to
	// persist). Idempotent under repeated bootstrap() calls because
	// bootstrapped=true short-circuits before any middleware wiring.
	installSessionMiddleware(a)

	dispatchModuleCallback(a.chainModules, func(mp chain.MiddlewareModule) {
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

	dispatchModuleCallback(a.chainModules, func(rp chain.RouteModule) {
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
		dispatchModuleCallback(a.chainModules, func(ep chain.EventModule) {
			ep.Events(a.Services.Events)
		})
		if a.eventsFn != nil {
			a.eventsFn(a.Services.Events)
		}
	} else {
		hasModuleEvents := false
		for _, p := range a.chainModules {
			if _, ok := p.(chain.EventModule); ok {
				hasModuleEvents = true
				break
			}
		}
		if a.eventsFn != nil || hasModuleEvents {
			a.Log.Warn("events are disabled via WithoutEvents; skipping event listener registration callbacks")
		}
	}

	// 5. Register scheduled jobs
	dispatchModuleCallback(a.chainModules, func(sp chain.ScheduleModule) {
		sp.Schedule(a.Services.Scheduler)
	})
	if a.scheduleFn != nil {
		a.scheduleFn(a.Services.Scheduler)
	}

	// 6. Register custom commands
	a.commands = chain.NewCommands()
	dispatchModuleCallback(a.chainModules, func(cp chain.CommandModule) {
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

	// Bridge contract.FailureEvent dispatches to the exception Reporter
	// chain. Wired on the dispatcher itself (not the dispatch closure) so
	// EVERY dispatch path is covered: service-fired events, registry
	// components, and app code calling Services.Events.Dispatch directly.
	// Optional-interface detection, same convention as the Aware sweeps;
	// a custom contract.Dispatcher without SetFailureReporter simply has
	// no bridge.
	if fr, ok := a.Services.Events.(interface {
		SetFailureReporter(fn func(ctx context.Context, event interface{}, err error))
	}); ok {
		fr.SetFailureReporter(buildFailureReporter(a))
	}

	wireComponentEvents(a)
}

// buildFailureReporter returns the bridge target for FailureEvent
// dispatches: it forwards the failure to ExceptionHandler.Report with an
// ExceptionContext carrying the trace ID and event name. It reads
// a.Services.Exceptions at call time, so a handler swapped in during a
// module Start phase wins.
func buildFailureReporter(a *App) func(ctx context.Context, event interface{}, err error) {
	return func(ctx context.Context, event interface{}, err error) {
		h := a.Services.Exceptions
		if h == nil {
			return
		}
		exCtx := &contract.ExceptionContext{
			Timestamp: time.Now(),
			TraceID:   trace.GetTraceID(ctx),
			Extra:     map[string]any{},
		}
		if n, ok := event.(interface{ Name() string }); ok {
			exCtx.Extra["event"] = n.Name()
		}
		h.Report(err, exCtx)
	}
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
// re-runs after each module lifecycle (WithModules in New, chain modules
// in bootstrap): modules
// register components only during Init/Start, so the New-time sweep would
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

// runModuleLifecycle executes the two-phase module startup: all Init() calls
// run first (bind services, no cross-module usage), then all Start() calls (wire
// dependencies, all services available). This ordering guarantees that Start() can
// safely reference services registered by other modules.
//
// The returned count is the number of modules whose Init COMPLETED, so
// failure-path unwinds can scope Shutdown to modules[:registered]. A module
// whose own Init returns an error is NOT counted (it must release anything
// it opened before returning), and modules after it never ran at all. On a
// Start failure every Init already completed, so the count is len(modules).
func runModuleLifecycle(modules []app.Module, services *app.Services, label string) (int, error) {
	for i, p := range modules {
		if err := p.Init(services); err != nil {
			return i, fmt.Errorf("velocity: %s init failed: %w", label, err)
		}
	}
	for _, p := range modules {
		if err := p.Start(services); err != nil {
			return len(modules), fmt.Errorf("velocity: %s start failed: %w", label, err)
		}
	}
	return len(modules), nil
}

// dispatchModuleCallback invokes fn on each module that implements the optional
// interface T (e.g., chain.RouteModule, chain.EventModule). This lets modules
// opt into lifecycle hooks without requiring every module to implement every
// interface.
func dispatchModuleCallback[T any](modules []app.Module, fn func(T)) {
	for _, p := range modules {
		if t, ok := any(p).(T); ok {
			fn(t)
		}
	}
}

// ErrCookieStoreInProduction is returned by App.Bootstrap when the app is
// running with APP_ENV unset / production AND the session scheme is using
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
// Skip in testing/development; skip when the active scheme is not the session
// scheme (JWT-only setups carry their own credentials); skip when a
// ServerSessionStore has been installed by an earlier Start() hook; skip when
// the operator opted in.
//
// The check runs at the END of bootstrap so modules that wire a
// ServerSessionStore in their Start() callback are honoured before the gate
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
	scheme, err := mgr.DefaultScheme()
	if err != nil {
		return nil
	}
	if _, ok := scheme.(*schemes.SessionScheme); !ok {
		return nil
	}
	if mgr.ServerSessionStore() != nil {
		return nil
	}
	return ErrCookieStoreInProduction
}

// installSessionMiddleware mounts schemes.SessionScheme.SessionMiddleware
// onto the router as the outermost global middleware when the active
// default auth scheme is a *SessionScheme. The fix for security audit H-05
// (CONFIRMED HIGH: "No save-at-end session middleware installed").
//
// Without this hook, every ctx.Auth().Session(r).Put / Flash call inside
// a handler is silently dropped because the cookie session store is only
// flushed by an explicit Session.Save(w). The middleware supplies that
// flush: run the inner handler, then save the session on the way out.
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
	scheme, err := mgr.DefaultScheme()
	if err != nil {
		return
	}
	sg, ok := scheme.(*schemes.SessionScheme)
	if !ok {
		return
	}
	a.Router.Use(sg.SessionMiddleware())
}

// installCSRFTokenRotator wires the final s.CSRF instance (post chain
// module Start) into the auth manager as a contract.CSRFTokenRotator so
// SessionScheme.Login regenerates the per-session CSRF token alongside the
// session id, SessionScheme.Logout revokes it before the session is
// invalidated, and the remember-cookie revival path rotates it across the
// recall regenerate. See contract.CSRFTokenRotator for the full contract.
//
// Start-order rationale: a chain module may legitimately replace s.CSRF
// in its Start() (custom store, different mode, decorator wrapping the
// framework-built instance). Running this install BEFORE Start would
// freeze the rotator to the original framework-built CSRF, and any
// subsequent consumer swap would leave the auth manager rotating a
// store no longer in the request path -> orphan tokens, first-POST 419.
// So this install runs AFTER runModuleLifecycle returns.
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
