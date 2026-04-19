package velocity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
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
	Version: "1.0.0-rc.1",
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
	// reference Services — putting Router inside Services would be circular.
	Router *router.VelocityRouterV2

	// Internal
	config         *Config
	server         *http.Server
	version        string
	noEvents       bool // skip event dispatcher initialization
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
// fails to initialize, New returns an error — it never panics.
//
// If an early stage succeeds and a later stage fails, every already-opened
// resource is closed via a deferred cleanup stack (logger file handles,
// DB pool, cache goroutines, queue workers, …). The cleanup stack runs in
// reverse registration order and every cleanup is best-effort — cleanup
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
	// resource down. On success — right before `return a, nil` — we
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

	// 2. Initialize exception handler (available for all subsequent services)
	a.Services.Exceptions = exceptions.NewHandler(
		exceptions.WithDebug(a.config.Debug),
		exceptions.WithEnvironment(a.config.Env),
	)

	// 3. Initialize crypto (auth/csrf may need it). Crypto is stateless
	// after construction — no cleanup needed.
	if a.config.Crypto.Key == "" {
		switch a.config.Env {
		case "testing":
			// Silent bypass — test harness wires its own keys as needed.
		case "development":
			a.Log.Warn("APP_KEY is unset — crypto subsystem disabled. Run `vel key:generate` before exercising auth/csrf/session flows.")
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
			a.Log.Warn("Insecure session cookie config (dev only — will fail in production)", "error", err)
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
			a.Log.Warn("Insecure CSRF cookie config (dev only — will fail in production)", "error", err)
		case "testing":
			// silent
		default:
			cancel()
			return nil, fmt.Errorf("velocity: %w", err)
		}
	}

	// 6. Initialize auth manager — pass DB for ORM provider. No cleanup
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

	// 10. Initialize queue — pass DB for database driver
	queueDriver, err := initQueue(a.config.Queue, sqlDB, a.config.DB.Connection, a.config.Queue.SigningKey, a.config.Key, a.Log)
	if err != nil {
		return nil, fmt.Errorf("velocity: failed to initialize queue: %w", err)
	}
	a.Queue = queueDriver
	cleanups = append(cleanups, func() {
		if a.Queue != nil {
			_ = a.Queue.Shutdown(context.Background())
		}
	})

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
	a.Router.SetServices(a.Services)
	a.Router.SetValidator(func(c *router.Context, rules map[string][]string, messages ...map[string]string) error {
		// Convert []string rules to pipe-separated format for validation package
		vRules := make(validation.Rules, len(rules))
		for field, fieldRules := range rules {
			vRules[field] = strings.Join(fieldRules, "|")
		}
		var msgs []validation.Messages
		for _, m := range messages {
			msgs = append(msgs, validation.Messages(m))
		}
		result := validation.CheckWithDB(c.Request, vRules, c.DB(), msgs...)
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
	// calling Shutdown in reverse registration order — same behaviour as
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
