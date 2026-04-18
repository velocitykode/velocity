package velocity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/app"
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
// a non-testing environment. The fix is to generate one via `vel key:generate`
// and set it in the environment before boot.
var ErrNoAppKey = errors.New("velocity: APP_KEY is required in non-testing environments (run `vel key:generate`)")

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
	providersFn    func(*ProviderRegistry)
	chainProviders []app.ServiceProvider
	middlewareFn   func(*MiddlewareStack)
	routesFn       func(*Routing)
	eventsFn       func(events.Dispatcher)
	scheduleFn     func(scheduler.TaskScheduler)
	commandsFn     func(*Commands)
	commands       *Commands
	exceptionsFn   func(exceptions.ExceptionHandler)
	bootstrapped   bool
}

// New creates a new Velocity application with all services initialized.
// Services are initialized in dependency order. If any required service
// fails to initialize, New returns an error — it never panics.
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

	// 1. Initialize logger first (everything else may need to log)
	logger, err := log.NewLogger(a.config.Log)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("velocity: failed to initialize logger: %w", err)
	}
	a.Log = logger

	// 2. Initialize exception handler (available for all subsequent services)
	a.Services.Exceptions = exceptions.NewHandler(
		exceptions.WithDebug(a.config.Debug),
		exceptions.WithEnvironment(a.config.Env),
	)

	// 3. Initialize crypto (auth/csrf may need it)
	if a.config.Crypto.Key == "" {
		if a.config.Env != "testing" {
			cancel()
			return nil, ErrNoAppKey
		}
	} else {
		enc, err := crypto.NewEncryptor(a.config.Crypto)
		if err != nil {
			cancel()
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
	}

	// 5. Initialize auth manager — pass DB for ORM provider
	var sqlDB *sql.DB
	if a.DB != nil {
		sqlDB = a.DB.DB()
	}
	a.Auth = initAuth(a.config.Auth, a.config.Session, a.Log, sqlDB, a.Crypto)

	// 6. Initialize cache
	a.Cache = initCache(a.config.Cache)

	// 7. Initialize CSRF
	a.CSRF = csrf.New(&a.config.CSRF)

	// 8. Initialize view/bond engine
	if a.config.View.RootTemplate != "" {
		viewEngine, err := view.NewEngine(a.config.View)
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize view engine: %w", err)
		}
		a.View = viewEngine
	}

	// 9. Initialize events dispatcher (skip if WithoutEvents was used, keep if pre-set by WithFakeEvents)
	if !a.noEvents && a.Services.Events == nil {
		a.Services.Events = events.NewDispatcher()
	}

	// 10. Initialize queue — pass DB for database driver
	queueDriver, err := initQueue(a.config.Queue, sqlDB, a.config.DB.Connection, a.config.Queue.SigningKey, a.config.Key, a.Log)
	if err != nil {
		return nil, fmt.Errorf("velocity: failed to initialize queue: %w", err)
	}
	a.Queue = queueDriver

	// 11. Initialize storage with disk drivers
	a.Storage = initStorage(a.config.Storage, a.Log)

	// 12. Initialize scheduler
	sched := scheduler.New()
	sched.SetEnv(a.config.Env)
	sched.SetLogger(a.Log)
	a.Scheduler = sched

	// 13. Initialize mail
	if a.config.Mail.Driver != "" {
		mailer, err := mail.NewMailer(a.config.Mail)
		if err != nil {
			a.Log.Warn("Failed to initialize mailer", "error", err)
		} else {
			a.Mail = mailer
		}
	}

	// 14. Initialize notification manager
	a.Notification = initNotification(a.Mail, sqlDB, a.config.DB.Connection)

	// 15. Create router and inject services
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

	// Run provider lifecycle: Register all, then Boot all
	if err := runProviderLifecycle(a.providers, a.Services, "provider"); err != nil {
		return nil, err
	}

	return a, nil
}

// Version returns the framework version.
func (a *App) Version() string {
	return a.version
}

// Run dispatches CLI commands or starts the HTTP server.
// Defined in run.go.

// --- Declarative bootstrap chain ---

// Providers registers a callback that adds service providers to the application.
// Providers registered here participate in the full bootstrap lifecycle including
// optional interfaces (RouteProvider, MiddlewareProvider, EventProvider, ScheduleProvider).
func (a *App) Providers(fn func(*ProviderRegistry)) *App {
	a.providersFn = fn
	return a
}

// Middleware registers a callback that configures the middleware stack.
func (a *App) Middleware(fn func(*MiddlewareStack)) *App {
	a.middlewareFn = fn
	return a
}

// Routes registers a callback that defines application routes.
func (a *App) Routes(fn func(*Routing)) *App {
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
func (a *App) Commands(fn func(*Commands)) *App {
	a.commandsFn = fn
	return a
}

// Exceptions registers a callback that configures the exception handler.
func (a *App) Exceptions(fn func(exceptions.ExceptionHandler)) *App {
	a.exceptionsFn = fn
	return a
}

