package velocity

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventqueue"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/trace"
)

// Bootstrap runs the declarative chain (modules, middleware, routes, events,
// schedule, commands, seeders, errors) without starting the HTTP server.
// Safe to call multiple times, but only the first call does the work. The
// result is sticky: after a successful run subsequent calls return nil, and
// after a failed run they return the same error (a partially-completed
// bootstrap is never re-run, because modules, middleware and routes
// registered before the failure would be registered twice).
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

	// 2a. Point the save-at-end session middleware New installed at the
	// default scheme as chain modules left it. See
	// schemes.SessionScheme.SessionMiddleware for the contract.
	refreshSessionScheme(a)

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
	// Lifecycle boundary: the Middleware, Routes and Events callbacks may
	// have replaced Services.Events (set it to nil included), replaced an
	// aware service instance or registered an aware component. The
	// dispatch closures are bound to the value wired before, so re-wire
	// every consumer to the dispatcher the app holds now.
	wireInstanceEvents(a)

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

	// 7. Register database seeders (consumed by `vel db seed`)
	a.seeders = chain.NewSeeders()
	dispatchModuleCallback(a.chainModules, func(sp chain.SeederModule) {
		sp.Seeders(a.seeders)
	})
	if a.seedersFn != nil {
		a.seedersFn(a.seeders)
	}

	// 8. Configure the error handler. The error page adapter resolves the
	// view engine per request; it is installed again here so a handler a
	// module swapped in since New gets it too.
	installErrorPageRenderer(a)
	if a.errorsFn != nil {
		a.errorsFn(a.Services.Errors)
	}
	// Last lifecycle boundary. The background failure reporters are bound
	// to a handler value (see wireFailureReporters) and the dispatch
	// closures to a dispatcher value; wireInstanceEvents re-binds both to
	// the ones the app holds now that every module and the Schedule,
	// Commands, Seeders and Errors callbacks have run.
	wireInstanceEvents(a)

	// 9. Refuse to run with CookieStore-only sessions in production
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
// the contract are skipped silently (e.g. when a feature is disabled). It
// also (re)installs the background failure reporters on the current error
// handler (see wireFailureReporters).
//
// The closure it hands out is bound to the dispatcher a.Services.Events
// holds now (see buildEventDispatch), so it runs, unconditionally, at every
// lifecycle boundary that can change that field or the set of consumers:
// in New before and after the WithModules lifecycle, and in bootstrap
// after the chain modules' Start, after the event registration step
// (Middleware, Routes and Events callbacks) and after the Errors step.
//
// Policy: at each boundary the app's current Services.Events wins for
// every consumer, including a router, service or component the app gave a
// different dispatcher inside an earlier callback; a nil Services.Events
// clears them all. The one exception is an app whose events were disabled
// from the start (WithoutEvents) and never wired: its sweeps leave every
// consumer as it is. The router's delivery mode is kept
// (BindEventDispatcher), so async delivery the app configured survives.
// An app that wants an independent sink on the router or a service
// configures it after the last boundary: call Bootstrap(), configure the
// sink, then Serve() (Serve skips the already-run bootstrap). Configuring
// it inside a bootstrap callback, or in one unbroken chain ending in
// Serve(), is overwritten.
//
// Every setter it calls is synchronized except the router's, which is
// only safe because every call happens before the router serves; router
// configuration calls must be serialized.
func wireInstanceEvents(a *App) {
	// The failure reporters follow the error handler, not the dispatcher,
	// so they are (re)installed whether or not events are enabled.
	wireFailureReporters(a)

	dispatch := buildEventDispatch(a)
	if dispatch == nil && !a.eventsWired {
		// Events disabled from the start (WithoutEvents): leave every
		// service without a dispatcher, as New constructed it.
		return
	}
	// A nil dispatch past this point means a module or callback set
	// Services.Events to nil after an earlier sweep wired it: clear the
	// earlier closure everywhere so dispatch becomes a no-op, as with
	// WithoutEvents, instead of reaching the replaced dispatcher. The flag
	// stays set, so every later boundary also clears a consumer introduced
	// after this sweep (a router async dispatcher or a component the app
	// configured with the old dispatcher) while Services.Events stays nil.
	if dispatch != nil {
		a.eventsWired = true
	}

	// Bind, not Set: an async delivery mode the app configured on the
	// router (SetAsyncEventDispatcher) survives the re-wire.
	a.Router.BindEventDispatcher(dispatch)

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

	wireComponentEvents(a, dispatch)
}

// wireFailureReporters installs the two reporters background failures reach
// the error handler through, both bound to the handler a.Services.Errors
// holds now, read once:
//
//   - the dispatcher's failure-report bridge, which reports every
//     contract.FailureEvent dispatch (job.failed, scheduled.failed, ...).
//     Wired on the dispatcher itself (not the dispatch closure) so every
//     dispatch path is covered: service-fired events, registry components,
//     and app code calling Services.Events.Dispatch directly. Optional
//     interface detection, same convention as the Aware sweeps: a custom
//     contract.Dispatcher without SetFailureReporter has no bridge, and with
//     events disabled there is none either.
//   - the queued-listener failure reporter a queued listener's Failed hook
//     calls once it has exhausted its retries. Installed with or without
//     events: a worker can run listener jobs another process queued.
//
// Both close over the handler value rather than reading a.Services.Errors
// when a failure happens: a worker a module Start launched runs on its own
// goroutine, and a later module Start replacing s.Errors would otherwise be
// an unsynchronized write against the worker's read. The setters behind
// both installs are synchronized, so re-running this while workers fail
// jobs only changes which handler later failures reach. It runs from
// wireInstanceEvents (in New before and after the WithModules lifecycle,
// and in bootstrap after the chain modules' Start) and once more at the end
// of bootstrap's error-handler step, so a handler a module swapped in or
// an Errors callback installed is the one reported to from then on. A
// worker already failing jobs before a re-install reports to the handler
// installed before it, which still reports; nothing is dropped by the
// switch.
func wireFailureReporters(a *App) {
	h := a.Services.Errors
	if fr, ok := a.Services.Events.(interface {
		SetFailureReporter(fn func(ctx context.Context, event interface{}, err error))
	}); ok {
		fr.SetFailureReporter(buildFailureReporter(h))
	}
	// Reporter only: the EventListenerJob factory New registered (app.go)
	// stays, so a factory a module's Start registered for the job is not
	// overwritten by a re-install.
	eventqueue.SetFailureReporter(buildQueuedListenerReporter(h))
}

// buildFailureReporter returns the bridge target for FailureEvent
// dispatches: it forwards the failure to h.Report with an ErrorContext
// carrying the trace ID and event name. It returns nil (no bridge) when h
// is nil.
func buildFailureReporter(h contract.ErrorHandler) func(ctx context.Context, event interface{}, err error) {
	if h == nil {
		return nil
	}
	return func(ctx context.Context, event interface{}, err error) {
		exCtx := &contract.ErrorContext{
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

// buildQueuedListenerReporter returns the reporter a queued listener's
// Failed hook calls once the listener has exhausted its retries (H-22): it
// reports the failure through h.TryReport with an ErrorContext naming the
// listener and event types, and returns TryReport's answer, whether the
// report was actually handled. The queue worker reads that answer
// (events.EventListenerJob.FailureReported): a failure this did not report
// is reported by the job.failed event's bridge instead. It returns nil (no
// reporter, so the hook reports nothing) when h is nil.
func buildQueuedListenerReporter(h contract.ErrorHandler) events.FailureReporter {
	if h == nil {
		return nil
	}
	return func(job *events.EventListenerJob, jobErr error) bool {
		exCtx := problem.NewErrorContext().
			WithExtra("subsystem", "events").
			WithExtra("job", "EventListenerJob").
			WithExtra("listener_type", job.ListenerType).
			WithExtra("event_type", job.EventType)
		return h.TryReport(jobErr, exCtx)
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

// buildEventDispatch returns the canonical dispatch closure with nil-ctx
// defaulting, bound to the dispatcher a.Services.Events holds now, read
// once, or nil when there is none (WithoutEvents) so callers can skip
// wiring entirely.
//
// The closure never reads a.Services.Events when it dispatches: services
// dispatch from goroutines a module Start may have launched (a queue push
// firing job.queued, a scheduler tick, an ORM event), and a later module
// Start assigning Services.Events would otherwise be an unsynchronized
// write against those reads. wireInstanceEvents re-runs at each lifecycle
// point that can change the field, so a replaced dispatcher is what later
// dispatches reach; a dispatch already in flight finishes on the one
// captured before.
func buildEventDispatch(a *App) func(ctx context.Context, event any) error {
	d := a.Services.Events
	if d == nil {
		return nil
	}
	return func(ctx context.Context, event any) error {
		if ctx == nil {
			ctx = context.Background()
		}
		return d.Dispatch(ctx, event)
	}
}

// wireComponentEvents wires the event dispatcher into every registry entry
// (registered value plus its hook adapters) that implements
// contract.EventDispatcherAware. It is called from wireInstanceEvents so it
// re-runs after each module lifecycle (WithModules in New, chain modules
// in bootstrap) and after bootstrap's event registration step: modules
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
//
// dispatch is the closure wireInstanceEvents built for this sweep (nil
// clears an earlier one), so components get the same dispatcher value as
// the services.
func wireComponentEvents(a *App, dispatch func(ctx context.Context, event any) error) {
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

// installSessionMiddleware mounts the save-at-end session middleware
// (schemes.SessionMiddlewareFor) onto the router as the outermost global
// middleware. New calls it, so every app serving through its router saves
// sessions at one point whether or not it calls Bootstrap or Serve. The
// fix for security audit H-05 (CONFIRMED HIGH: "No save-at-end session
// middleware installed").
//
// It goes to the front of the global list (Router.UseFirst), ahead of
// middleware the app adds with Router.Use. A buffering middleware outside
// it (bond's, the router's Timeout) would hide the response writer's
// pre-commit hook, so the session would save as soon as the handler
// returned and miss what the error page writes afterwards, such as a
// drained flash.
//
// The middleware serves the scheme a.sessionScheme holds at request time:
// the default auth scheme when it is a *schemes.SessionScheme. JWT-only
// or custom auth managers leave it nil and the middleware passes straight
// through (no session bag to persist).
func installSessionMiddleware(a *App) {
	if a.Router == nil {
		return
	}
	refreshSessionScheme(a)
	a.Router.UseFirst(schemes.SessionMiddlewareFor(a.sessionScheme.Load))
}

// refreshSessionScheme points the save seam at the current default auth
// scheme when it is a *schemes.SessionScheme, and at nothing otherwise.
// We type-assert through contract.AuthManager because a.Services.Auth is
// the public interface (the auth/csrf/view packages cannot import each
// other directly without a cycle).
func refreshSessionScheme(a *App) {
	a.sessionScheme.Store(defaultSessionScheme(a))
}

// defaultSessionScheme returns a's default auth scheme when it is a
// *schemes.SessionScheme, or nil.
func defaultSessionScheme(a *App) *schemes.SessionScheme {
	if a.Services == nil || a.Auth == nil {
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
	sg, _ := scheme.(*schemes.SessionScheme)
	return sg
}

// csrfSessionResolver returns the CSRF SessionIDResolver New installs
// when CSRF binds to the session cookie. It answers with the id of the
// session the request is served under, and only with a session the
// session store accepts:
//
//   - Inside the session middleware it returns the id of the session the
//     middleware attached. That covers an anonymous visitor's first GET:
//     the middleware mints the session before the CSRF safe-method
//     bootstrap runs, so XSRF-TOKEN is written for the id the response
//     is about to set.
//   - Otherwise (the CSRF middleware mounted outside a.Router, or the
//     resolver called directly) it loads the session through the current
//     session scheme, the same store Get every handler uses, which
//     rejects a cookie that fails to decrypt, was revoked at logout or is
//     past its lifetime. The store answers a rejected or missing cookie
//     with a freshly created session, which is born modified; that
//     answer, a session that cannot report whether it is fresh, and no
//     session scheme all return csrf.ErrNoSession, so no token is minted
//     for or accepted from a session the client does not hold.
func csrfSessionResolver(current func() *schemes.SessionScheme) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		if sess := schemes.SessionFromRequest(r); sess != nil {
			if id := sess.ID(); id != "" {
				return id, nil
			}
		}
		scheme := current()
		if scheme == nil {
			return "", csrf.ErrNoSession
		}
		sess := scheme.Session(r)
		if sess == nil || sess.ID() == "" {
			return "", csrf.ErrNoSession
		}
		fresh, ok := sess.(interface{ IsModified() bool })
		if !ok || fresh.IsModified() {
			return "", csrf.ErrNoSession
		}
		return sess.ID(), nil
	}
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
