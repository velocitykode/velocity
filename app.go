package velocity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/view"
)

// BuildInfo carries version metadata baked in via -ldflags. See the Makefile
// build target for the ldflag incantation; defaults below apply to `go run`.
var BuildInfo = struct {
	Version string
	Commit  string
	Date    string
}{
	Version: "devel",
	Commit:  "devel",
	Date:    "unknown",
}

// ErrNoAppKey is returned from New when APP_KEY (or CRYPTO_KEY) is unset in
// a non-testing, non-development environment. The fix is to generate one
// via `vel key:generate` and set it in the environment before boot.
var ErrNoAppKey = errors.New("velocity: APP_KEY is required outside APP_ENV=testing or APP_ENV=development (run `vel key:generate`)")

// App represents the Velocity application container.
// It owns all framework subsystem instances and provides them to the consumer.
type App struct {
	// Services contains all non-router services, shared with router.Context.
	*app.Services

	// Router is separate from Services because it creates contexts that
	// reference Services, putting Router inside Services would be circular.
	Router *router.VelocityRouterV2

	// Internal
	config         *Config
	server         *http.Server
	version        string
	noEvents       bool // skip event dispatcher initialization
	runScheduler   bool // start scheduler in-process under Serve() (WithSchedulerInProcess)
	providers      []app.ServiceProvider
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc

	// Declarative bootstrap chain
	providersFn    func(*chain.ProviderRegistry)
	chainProviders []app.ServiceProvider
	middlewareFn   func(*chain.MiddlewareStack)
	routesFn       func(*chain.Routing)
	eventsFn       func(events.Dispatcher)
	scheduleFn     func(scheduler.TaskScheduler)
	commandsFn     func(*chain.Commands)
	commands       *chain.Commands
	exceptionsFn   func(exceptions.ExceptionHandler)
	bootstrapped   bool

	// outboxRelay is an optional ORM transactional-outbox relay registered
	// via UseOutboxRelay. Shutdown stops it before tearing down the queue
	// and database so in-flight dispatches can complete.
	outboxRelay *orm.Relay

	// serveHTTPHook is a test-only seam used by the regression test for
	// the serveRunCmd → Serve() recursion bug. When non-nil, serveHTTP()
	// invokes the hook and returns its result instead of booting services
	// and blocking on the HTTP listener. The field is unexported, has no
	// setter on the public surface, and is never assigned by production
	// code; tests in this package assign to it directly.
	serveHTTPHook func() error
}

// New creates a new Velocity application with all services initialized.
// Services are initialized in dependency order. If any required service
// fails to initialize, New returns an error, it never panics.
//
// If an early stage succeeds and a later stage fails, every already-opened
// resource is closed via a deferred cleanup stack (logger file handles,
// DB pool, cache goroutines, queue workers, …). The cleanup stack runs in
// reverse registration order and every cleanup is best-effort, cleanup
// failures are logged (where a logger is available) but do not replace the
// original error returned to the caller.
func New(opts ...Option) (*App, error) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{
		Services: &app.Services{
			Extensions: make(map[string]any),
		},
		version:        BuildInfo.Version,
		shutdownCtx:    ctx,
		shutdownCancel: cancel,
	}

	// Load config from env by default
	config := ConfigFromEnv()
	a.config = &config

	// Apply options (may override config)
	for _, opt := range opts {
		opt(a)
	}

	// cleanups is the deferred teardown stack for the failure path.
	// Each successful resource init appends a closure that shuts the
	// resource down. On success, right before `return a, nil`, we
	// assign cleanups = nil so the deferred closure is a no-op. On
	// failure, the deferred closure walks the stack in reverse so
	// later resources are torn down before earlier ones (same order
	// as App.Shutdown).
	var cleanups []func()
	defer func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}()
	// The shutdown context must always be cancelled on the failure path
	// so any goroutine observing shutdownCtx.Done() (e.g. a BaseContext
	// consumer spawned by a provider) unwinds promptly. On the success
	// path, Shutdown() cancels it.
	cleanups = append(cleanups, func() { cancel() })

	// 1. Initialize logger first (everything else may need to log)
	logger, err := log.NewLogger(a.config.Log)
	if err != nil {
		return nil, fmt.Errorf("velocity: failed to initialize logger: %w", err)
	}
	a.Log = logger
	cleanups = append(cleanups, func() {
		if sd, ok := a.Log.(contract.ShutdownAware); ok {
			_ = sd.Shutdown(context.Background())
		} else if closer, ok := a.Log.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	})

	// 2. Initialize exception handler (available for all subsequent services).
	//
	// Trusted-proxy plumbing: parse the deployment-level trust list once
	// here and propagate to every subsystem that captures a client IP
	// (exceptions audit log, router rate-limit, auth throttler). The
	// list is sourced from Config.Auth.TrustedProxies for now; the auth
	// config is where the C-05 fix introduced the deployment knob and
	// it is still the single source of truth for "real client IP" until
	// a follow-up consolidates it to the root Config.
	//
	// A malformed entry is logged once and the list is dropped (no
	// proxies trusted, secure default). Operators who want fail-fast
	// startup should validate at boot via clientip.ParseCIDRs.
	//
	// CRITICAL: every consumer receives its OWN deep clone (via
	// clientip.CloneIPNets at each call site). The parsed list, the
	// app.config.Auth.TrustedProxies string slice, and any caller-side
	// retained reference therefore cannot influence consumers'
	// runtime trust decisions. Each setter also deep-clones on its
	// write path as belt-and-braces, but cloning at wiring time makes
	// the intent explicit at the boundary where the decision is made.
	trustedProxyNets, tpErr := clientip.ParseCIDRs(a.config.Auth.TrustedProxies)
	if tpErr != nil {
		a.Log.Warn("Trusted proxies parse failed; XFF headers will be untrusted everywhere", "error", tpErr)
		trustedProxyNets = nil
	}

	a.Services.Exceptions = exceptions.NewHandler(
		exceptions.WithDebug(a.config.Debug),
		exceptions.WithEnvironment(a.config.Env),
		exceptions.WithTrustedProxies(clientip.CloneIPNets(trustedProxyNets)),
	)

	// 3. Initialize crypto (auth/csrf may need it). Crypto is stateless
	// after construction, no cleanup needed.
	if a.config.Crypto.Key == "" {
		switch a.config.Env {
		case "testing":
			// Silent bypass, test harness wires its own keys as needed.
		case "development":
			a.Log.Warn("APP_KEY is unset, crypto subsystem disabled. Run `vel key:generate` before exercising auth/csrf/session flows.")
		default:
			return nil, ErrNoAppKey
		}
	} else {
		enc, err := crypto.NewEncryptor(a.config.Crypto)
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize crypto: %w", err)
		}
		a.Crypto = enc
	}

	// 4. Initialize database connection
	dbManager, err := initDB(a.config.DB)
	if err != nil {
		return nil, fmt.Errorf("velocity: failed to initialize database: %w", err)
	}
	if dbManager != nil {
		a.DB = dbManager
		orm.SetDefault(dbManager)
		cleanups = append(cleanups, func() {
			_ = a.DB.Shutdown(context.Background())
			orm.ResetDefault()
		})
	}

	// 5. Validate cookie-related configs (session, CSRF). Fail-loud in
	// production; warn in development. Testing env is permissive so
	// bare-minimum test setups keep working.
	if err := a.config.Session.Validate(a.config.Env); err != nil {
		switch a.config.Env {
		case "development":
			a.Log.Warn("Insecure session cookie config (dev only, will fail in production)", "error", err)
		case "testing":
			// silent
		default:
			cancel()
			return nil, fmt.Errorf("velocity: %w", err)
		}
	}
	if err := a.config.CSRF.Validate(a.config.Env); err != nil {
		switch a.config.Env {
		case "development":
			a.Log.Warn("Insecure CSRF cookie config (dev only, will fail in production)", "error", err)
		case "testing":
			// silent
		default:
			cancel()
			return nil, fmt.Errorf("velocity: %w", err)
		}
	}

	// 6. Initialize auth manager, pass DB for ORM provider. No cleanup
	// registration: *auth.Manager does not currently expose Shutdown.
	// JWT guard cleanup goroutines are tied to the process lifetime.
	var sqlDB *sql.DB
	if a.DB != nil {
		sqlDB = a.DB.DB()
	}
	a.Auth = initAuth(a.config.Auth, a.config.Session, a.Log, sqlDB, a.Crypto)

	// 7. Initialize cache
	a.Cache = initCache(a.config.Cache)
	cleanups = append(cleanups, func() {
		if a.Cache != nil {
			_ = a.Cache.Shutdown(context.Background())
		}
	})

	// 8. Initialize CSRF
	//
	// Inject a SessionIDResolver that decrypts the session cookie directly
	// and returns the plaintext session ID. The CSRF token store is keyed
	// by this ID; without the resolver it would be keyed by the per-response
	// ciphertext cookie value, which rotates on every Save() and causes
	// 419 on the next state-changing request.
	//
	// The resolver MUST refuse to mint or accept tokens for requests that
	// carry no real session cookie. Calling auth.Manager.Session(r) here
	// would silently create an ephemeral session (auth/session.go's
	// GetSessionFromRequest / CookieStore.Get both fall back to
	// store.Create("") on missing/invalid cookies), reintroducing the
	// exact attack surface TestCSRF_RefusesEphemeralSession pins. So we
	// require the cookie to exist AND decrypt successfully; anything else
	// returns ErrNoSession.
	//
	// Install the auto-resolver ONLY when CSRF is binding to the
	// built-in auth session cookie. Two cases must not auto-wire:
	//
	//   - CSRF_SESSION_COOKIE points at a different cookie (the operator
	//     is intentionally binding CSRF to a non-session cookie, plain
	//     or encrypted under a different scheme). Decrypting it with the
	//     app encryptor would 419 every request.
	//   - The app does not use the built-in session cookie at all
	//     (a.config.Session.Name is empty). Same outcome.
	//
	// In both skipped cases we install a strict-reject resolver that
	// always returns ErrNoSession. csrf.NewE rejects nil resolvers because
	// keying CSRF tokens by an unauthenticated cookie value is unsafe;
	// the strict-reject resolver fails closed (all unsafe requests 419)
	// until the operator wires a real resolver via Config.SessionIDResolver.
	if a.config.CSRF.SessionIDResolver == nil &&
		a.Crypto != nil &&
		a.config.Session.Name != "" &&
		a.config.CSRF.SessionCookieName == a.config.Session.Name {
		encryptor := a.Crypto
		sessionCookieName := a.config.Session.Name
		a.config.CSRF.SessionIDResolver = func(r *http.Request) (string, error) {
			c, err := r.Cookie(sessionCookieName)
			if err != nil || c.Value == "" {
				return "", csrf.ErrNoSession
			}
			plaintext, err := encryptor.Decrypt(c.Value)
			if err != nil {
				return "", csrf.ErrNoSession
			}
			// CookieStore wire format: {"id":"...","data":{...},"flash":{...}}.
			// Only the id is needed to key CSRF tokens.
			var payload struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(plaintext), &payload); err != nil || payload.ID == "" {
				return "", csrf.ErrNoSession
			}
			return payload.ID, nil
		}
	} else if a.config.CSRF.SessionIDResolver == nil {
		a.config.CSRF.SessionIDResolver = func(r *http.Request) (string, error) {
			return "", csrf.ErrNoSession
		}
	}
	csrfInstance, err := csrf.NewE(&a.config.CSRF)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("velocity: failed to initialize csrf: %w", err)
	}
	a.CSRF = csrfInstance
	cleanups = append(cleanups, func() {
		if sd, ok := a.CSRF.(contract.ShutdownAware); ok {
			_ = sd.Shutdown(context.Background())
		}
	})

	// Wire the CSRF token rotator into the auth manager so direct
	// New() consumers (no Bootstrap(), no Serve()) still get session
	// lifecycle rotation: Login regenerates the per-session token,
	// Logout revokes it, the remember-cookie revival path rotates it
	// across recall. This covers embed-mode apps, tests, and scripts
	// that hold an *App without calling the declarative chain.
	//
	// bootstrap() re-runs the same install AFTER chain providers' Boot
	// so consumers that legitimately replace s.CSRF in their Boot have
	// their replacement honored. The install helper is idempotent
	// (just sets a function pointer on auth.Manager protected by mu),
	// so calling it twice is safe; the last call wins, which is the
	// bootstrap-time call when a chain provider participated.
	//
	// See installCSRFTokenRotator in bootstrap.go,
	// TestCSRFRotator_PointsToBootReplacement (chain-provider path),
	// and TestCSRFRotator_WiredByNewWithoutBootstrap (direct-New path).
	installCSRFTokenRotator(a)

	// 8. Initialize view/bond engine
	if a.config.View.RootTemplate != "" {
		viewEngine, err := view.NewEngine(a.config.View)
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize view engine: %w", err)
		}
		a.View = viewEngine
		cleanups = append(cleanups, func() {
			if sd, ok := a.View.(contract.ShutdownAware); ok {
				_ = sd.Shutdown(context.Background())
			}
		})
	}

	// 9. Initialize events dispatcher (skip if WithoutEvents was used, keep if pre-set by WithFakeEvents).
	// The dispatcher itself has no Shutdown today; the router drains async
	// workers via ShutdownEventDispatcher once wired (see wireInstanceEvents).
	if !a.noEvents && a.Services.Events == nil {
		a.Services.Events = events.NewDispatcher()
	}

	// 10. Initialize queue, pass DB for database driver
	queueDriver, err := initQueue(a.config.Queue, sqlDB, a.config.DB.Connection, a.config.Queue.SigningKey, a.config.Key, a.config.Env, a.Log)
	if err != nil {
		return nil, fmt.Errorf("velocity: failed to initialize queue: %w", err)
	}
	a.Queue = queueDriver
	cleanups = append(cleanups, func() {
		if a.Queue != nil {
			_ = a.Queue.Shutdown(context.Background())
		}
		// C-03-fb2 HIGH 2: drop the auto-installed batch repository
		// and clear the global hooks on failure-path teardown. The
		// happy-path Shutdown does this in serve.go; here we mirror it
		// so a partial New() failure does not leave a dangling repo
		// for the next attempt.
		queue.ResetAutoInstalledBatchRepository()
		queue.SetGlobalEventDispatcher(nil)
		queue.SetBatchCallbackQueue(nil, "")
		// H-22: clear the queued-listener failure reporter so a new
		// app instance does not inherit a stale callback bound to the
		// previous Exceptions handler.
		events.InitializeQueueIntegration(nil, nil, nil)
	})

	// H-22: register the EventListenerJob factory with the queue's typed
	// job registry, and wire the failure reporter to the framework's
	// exceptions handler so queued listeners that exhaust retries surface
	// to the configured reporters instead of being silently dropped.
	// Idempotent on repeated calls. The dispatcher argument is nil because
	// the default events.Dispatcher is *DefaultDispatcher, not
	// *QueueIntegratedDispatcher; consumers that opt into the
	// queue-integrated dispatcher are expected to call
	// InitializeQueueIntegration themselves with their dispatcher to bind
	// the queue driver.
	var reporter events.FailureReporter
	if a.Services.Exceptions != nil {
		exHandler := a.Services.Exceptions
		reporter = func(job *events.EventListenerJob, jobErr error) {
			exCtx := exceptions.NewExceptionContext().
				WithExtra("subsystem", "events").
				WithExtra("job", "EventListenerJob").
				WithExtra("listener_type", job.ListenerType).
				WithExtra("event_type", job.EventType)
			exHandler.Report(jobErr, exCtx)
		}
	}
	events.InitializeQueueIntegration(nil, a.Queue, reporter)

	// 11. Initialize storage with disk drivers
	a.Storage = initStorage(a.config.Storage, a.Log)
	cleanups = append(cleanups, func() {
		if sd, ok := a.Storage.(contract.ShutdownAware); ok {
			_ = sd.Shutdown(context.Background())
		}
	})

	// 12. Initialize scheduler
	sched := scheduler.New()
	sched.SetEnv(a.config.Env)
	sched.SetLogger(a.Log)
	// Wire a cache-backed Locker when the configured cache driver
	// supports the cache Lock primitive (Redis today). Without this,
	// scheduler.New defaults to InMemoryLocker, which means
	// OnOneServer() and WithoutOverlapping() degrade to single-process
	// semantics across an HA pair -- the C-04 worst case. Drivers that
	// do not implement Lock (file, database) fall back to
	// InMemoryLocker with a WARN; otherwise cacheLocker.Acquire would
	// surface a misconfiguration error that the scheduler treats as
	// contention, silently skipping every guarded job. Memory-cache
	// deployments retain InMemoryLocker (single-process scope matches
	// the cache's scope).
	installSchedulerLocker(sched, a.Cache, a.config.Cache.Driver, a.Log)
	a.Scheduler = sched
	cleanups = append(cleanups, func() {
		if a.Scheduler != nil {
			_ = a.Scheduler.Shutdown(context.Background())
		}
	})

	// 13. Initialize mail
	if a.config.Mail.Driver != "" {
		mailer, err := mail.NewMailer(a.config.Mail)
		if err != nil {
			a.Log.Warn("Failed to initialize mailer", "error", err)
		} else {
			a.Mail = mailer
			cleanups = append(cleanups, func() {
				if sd, ok := a.Mail.(contract.ShutdownAware); ok {
					_ = sd.Shutdown(context.Background())
				}
			})
		}
	}

	// 14. Initialize notification manager
	a.Notification = initNotification(a.Mail, sqlDB, a.config.DB.Connection)
	cleanups = append(cleanups, func() {
		if sd, ok := a.Notification.(contract.ShutdownAware); ok {
			_ = sd.Shutdown(context.Background())
		}
	})

	// 15. Create router and inject services. The router has no external
	// resources at this point (no listener bound) so no cleanup is needed.
	a.Router = router.New()
	// Publish the router as the canonical redirect-allowlist source so
	// bond (and any future redirect helper that cannot import router)
	// can consult Router.RedirectAllowedHosts via the contract interface
	// instead of trusting an operator-spoofable r.Host. Set BEFORE
	// SetServices so the router-owned services struct sees the value
	// immediately, and any pooled Context carries the same Services
	// pointer.
	a.Services.RedirectAllowlist = a.Router
	a.Router.SetServices(a.Services)
	// Propagate the deployment-level trusted-proxy list parsed at step 2
	// so Context.IP(), per-IP rate limits, and any future client-IP
	// surface in the router agree with the throttle/exception layers.
	//
	// Copy the []string slice (defensively) AND immediately force a
	// parse via ValidateConfig so the router holds its own parsed
	// *TrustedProxies. Aliasing app.config.Auth.TrustedProxies would
	// let any later mutation of that slice (or its strings) flip
	// router trust decisions at runtime. Validation failure here is
	// logged and the list is dropped (no proxies trusted, secure
	// default), matching the exceptions/auth wiring above.
	if len(a.config.Auth.TrustedProxies) > 0 {
		copied := make([]string, len(a.config.Auth.TrustedProxies))
		copy(copied, a.config.Auth.TrustedProxies)
		a.Router.TrustedProxies = copied
		if err := a.Router.ValidateConfig(); err != nil {
			a.Log.Warn("Router trusted proxies parse failed; XFF headers will be untrusted in the router", "error", err)
			// Drop the raw list so the lazy-parse path that runs on
			// first request also yields an empty trust set, not a
			// partial / broken one.
			a.Router.TrustedProxies = nil
		}
	}
	// Configure the file-serving root. When FILE_ROOT is unset, the
	// router falls back to the process CWD at request time, preserving
	// legacy behaviour for callers that have not opted in.
	a.Router.SetFileRoot(a.config.FileRoot)
	// Derive the signed-URL HMAC subkey from APP_KEY so router.SignedURL /
	// router.ValidateSignature work without leaking the master key into
	// every Context. HKDF separates this subsystem from the queue signing
	// key, the maintenance-bypass MAC, and cookie encryption: a forged
	// signature on one subkey grants nothing on the others.
	//
	// M-16 defence-in-depth: production refuses to boot when APP_KEY is
	// empty even if CRYPTO_KEY is set. The previous behaviour silently
	// skipped derivation here whenever a.config.Key was empty, and the
	// router middleware then failed open, so a protected signed route
	// downgraded to an unsigned route. Mirror the APP_KEY check at line
	// ~190 (Crypto.Key gating): permit testing/development to run
	// without APP_KEY so local-dev does not require `vel key:generate`,
	// but every other environment must have APP_KEY set explicitly.
	if a.config.Key == "" {
		switch a.config.Env {
		case "testing":
			// Silent bypass; test harnesses wire keys explicitly via
			// router.SetSignedURLKey when they need signed URLs.
		case "development":
			a.Log.Warn("APP_KEY is unset, router signed-URL middleware will fail closed (403) on every signed route. Run `vel key:generate` before exercising signed-URL flows.")
		default:
			cancel()
			return nil, fmt.Errorf("velocity: %w", ErrNoAppKey)
		}
	} else {
		signedKey, err := router.DeriveSignedURLKey([]byte(a.config.Key))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("velocity: failed to derive signed URL key: %w", err)
		}
		a.Router.SetSignedURLKey(signedKey)
	}
	a.Router.SetValidator(func(c *router.Context, rules map[string][]string, messages ...map[string]string) error {
		// rules is the canonical Rules slice form; pass through directly.
		var msgs []validation.Messages
		for _, m := range messages {
			msgs = append(msgs, validation.Messages(m))
		}
		// CheckWithDBW threads c.Response into the validation body-read
		// path so http.MaxBytesReader can fire its requestTooLarge
		// connection-close hint on oversized bodies (rule 5: limit all
		// request body reads).
		result := validation.CheckWithDBW(c.Response, c.Request, validation.Rules(rules), c.DB(), msgs...)
		if !result.HasErrors() {
			return nil
		}
		c.WithErrors(result.All())
		c.WithInput(result.Old())
		if v := c.View(); v != nil {
			v.Back(c.Response, c.Request)
		}
		return router.ErrValidationAborted
	})

	// 16. Initialize validator
	a.Validator = validation.NewValidator()

	// Wire event dispatchers into service instances
	wireInstanceEvents(a)

	// Run provider lifecycle: Register all, then Boot all. On failure,
	// providers that already completed Register/Boot will be unwound by
	// calling Shutdown in reverse registration order, same behaviour as
	// App.Shutdown so consumers see a single, consistent teardown.
	if err := runProviderLifecycle(a.providers, a.Services, "provider"); err != nil {
		cleanups = append(cleanups, func() {
			shutdownCtx := context.Background()
			for i := len(a.providers) - 1; i >= 0; i-- {
				_ = a.providers[i].Shutdown(shutdownCtx)
			}
		})
		return nil, err
	}

	// Success path: disarm the cleanup stack. From here on, resources are
	// owned by the *App and released via Shutdown().
	cleanups = nil
	return a, nil
}

// Version returns the framework version.
func (a *App) Version() string {
	return a.version
}

// Run dispatches CLI commands or starts the HTTP server.
// Defined in cmd.go.

// --- Declarative bootstrap chain ---

// Providers registers a callback that adds service providers to the application.
// Providers registered here participate in the full bootstrap lifecycle including
// optional interfaces (chain.RouteProvider, chain.MiddlewareProvider,
// chain.EventProvider, chain.ScheduleProvider, chain.CommandProvider).
func (a *App) Providers(fn func(*chain.ProviderRegistry)) *App {
	a.providersFn = fn
	return a
}

// Middleware registers a callback that configures the middleware stack.
func (a *App) Middleware(fn func(*chain.MiddlewareStack)) *App {
	a.middlewareFn = fn
	return a
}

// Routes registers a callback that defines application routes.
func (a *App) Routes(fn func(*chain.Routing)) *App {
	a.routesFn = fn
	return a
}

// Events registers a callback that configures event listeners.
func (a *App) Events(fn func(events.Dispatcher)) *App {
	a.eventsFn = fn
	return a
}

// Schedule registers a callback that configures scheduled jobs.
func (a *App) Schedule(fn func(scheduler.TaskScheduler)) *App {
	a.scheduleFn = fn
	return a
}

// Commands registers a callback that adds custom commands to the application.
// Commands are invokable via `vel run <name>`.
func (a *App) Commands(fn func(*chain.Commands)) *App {
	a.commandsFn = fn
	return a
}

// Exceptions registers a callback that configures the exception handler.
func (a *App) Exceptions(fn func(exceptions.ExceptionHandler)) *App {
	a.exceptionsFn = fn
	return a
}

// UseOutboxRelay registers an ORM transactional-outbox relay that will be
// stopped during App.Shutdown. Auto-start is the caller's responsibility
// (call relay.Start before Serve); this only wires teardown.
func (a *App) UseOutboxRelay(r *orm.Relay) *App {
	a.outboxRelay = r
	return a
}
