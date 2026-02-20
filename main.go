package velocity

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/notification/channels"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/storage"
	"github.com/velocitykode/velocity/validate"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/view"
)

const frameworkVersion = "0.1.0"

// App represents the Velocity application container.
// It owns all framework subsystem instances and provides them to the consumer.
type App struct {
	// Services contains all non-router services, shared with router.Context.
	*app.Services

	// Router is separate from Services because it creates contexts that
	// reference Services — putting Router inside Services would be circular.
	Router *router.VelocityRouterV2

	// Internal
	config    *Config
	server    *http.Server
	version   string
	providers []app.ServiceProvider

	// Declarative bootstrap chain
	providersFn    func(*ProviderRegistry)
	chainProviders []app.ServiceProvider
	middlewareFn   func(*MiddlewareStack)
	routesFn       func(*Routing)
	eventsFn       func(events.Dispatcher)
	scheduleFn     func(*scheduler.Scheduler)
	exceptionsFn   func(*exceptions.Handler)
	bootstrapped   bool
}

// New creates a new Velocity application with all services initialized.
// Services are initialized in dependency order. If any required service
// fails to initialize, New returns an error — it never panics.
func New(opts ...Option) (*App, error) {
	a := &App{
		Services: &app.Services{
			Extensions: make(map[string]any),
		},
		version:  frameworkVersion,
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
		logger, _ = log.NewLogger(log.LogConfig{Driver: "console"})
	}
	a.Log = logger

	// 2. Initialize crypto (auth/csrf may need it)
	if a.config.Crypto.Key != "" {
		enc, err := crypto.NewEncryptor(a.config.Crypto)
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize crypto: %w", err)
		}
		a.Crypto = enc
	}

	// 3. Initialize database connection
	if a.config.DB.Connection != "" {
		dbManager, err := orm.NewManager(orm.ManagerConfig{
			Driver:          a.config.DB.Connection,
			Host:            a.config.DB.Host,
			Port:            a.config.DB.Port,
			Database:        a.config.DB.Database,
			Username:        a.config.DB.Username,
			Password:        a.config.DB.Password,
			Charset:         a.config.DB.Charset,
			SSLMode:         a.config.DB.SSLMode,
			MaxIdleConns:    a.config.DB.MaxIdleConns,
			MaxOpenConns:    a.config.DB.MaxOpenConns,
			ConnMaxLifetime: a.config.DB.ConnMaxLifetime,
			LogQueries:      a.config.DB.LogQueries,
			SlowThreshold:   a.config.DB.SlowThreshold,
		})
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize database: %w", err)
		}
		a.DB = dbManager
		orm.SetDefault(dbManager)
	}

	// 4. Initialize auth manager — pass DB for ORM provider
	var sqlDB *sql.DB
	if a.DB != nil {
		sqlDB = a.DB.DB()
	}
	a.Auth = initAuth(a.config.Auth, a.config.Session, a.Log, sqlDB, a.Crypto)

	// 5. Initialize cache
	a.Cache = initCache(a.config.Cache)

	// 6. Initialize CSRF
	a.CSRF = csrf.New(nil)

	// 7. Initialize view/bond engine
	if a.config.View.RootTemplate != "" {
		viewEngine, err := view.NewEngine(a.config.View)
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize view engine: %w", err)
		}
		a.View = viewEngine
	}

	// 8. Initialize events dispatcher
	a.Services.Events = events.NewDispatcher()

	// 9. Initialize queue — pass DB for database driver
	a.Queue = initQueue(a.config.Queue, sqlDB)

	// 10. Initialize storage with disk drivers
	a.Storage = initStorage(a.config.Storage, a.Log)

	// 11. Initialize scheduler
	a.Scheduler = scheduler.New()

	// 12. Initialize mail
	if a.config.Mail.Driver != "" {
		mailer, err := mail.NewMailer(a.config.Mail)
		if err != nil {
			a.Log.Warn("Failed to initialize mailer", "error", err)
		} else {
			a.Mail = mailer
		}
	}

	// 13. Initialize notification manager
	a.Notification = initNotification(a.Mail, sqlDB, a.config.DB.Connection)

	// 14. Create router and inject services
	a.Router = router.New()
	a.Router.SetServices(a.Services)
	a.Router.SetValidator(func(c *router.Context, rules map[string][]string, messages ...map[string]string) {
		var msgs []validate.Messages
		for _, m := range messages {
			msgs = append(msgs, validate.Messages(m))
		}
		errors := validate.CheckWithDB(c.Request, validate.Rules(rules), c.DB(), msgs...)
		if !errors.HasErrors() {
			return
		}
		c.WithErrors(errors.All())
		c.WithInput(errors.Old())
		type backer interface {
			Back(http.ResponseWriter, *http.Request)
		}
		if v, ok := c.View().(backer); ok {
			v.Back(c.Response, c.Request)
		}
		panic(router.AbortValidation{})
	})

	// 15. Initialize exception handler
	a.Services.Exceptions = exceptions.NewHandler(
		exceptions.WithDebug(a.config.Debug),
		exceptions.WithEnvironment(a.config.Env),
	)

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

// Serve starts the HTTP server with signal handling and graceful shutdown.
func (a *App) Serve() error {
	if err := a.bootstrap(); err != nil {
		return err
	}

	addr := ":" + a.config.Port
	a.server = &http.Server{
		Addr:         addr,
		Handler:      a.Router,
		ReadTimeout:  a.config.ReadTimeout,
		WriteTimeout: a.config.WriteTimeout,
		IdleTimeout:  a.config.IdleTimeout,
	}

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		a.Log.Info("Velocity server started", "version", a.version, "addr", addr)
		if err := a.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("velocity: server error: %w", err)
	case sig := <-quit:
		a.Log.Info("Shutting down server", "signal", sig.String())
	}

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return a.Shutdown(ctx)
}

// Shutdown gracefully shuts down all services in reverse initialization order.
func (a *App) Shutdown(ctx context.Context) error {
	var firstErr error
	setErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// 1. Stop accepting new connections
	if a.server != nil {
		setErr(a.server.Shutdown(ctx))
	}

	// 2. Stop scheduler
	if a.Scheduler != nil {
		a.Scheduler.Stop()
	}

	// 3. Close queue driver
	if a.Queue != nil {
		setErr(a.Queue.Close())
	}

	// 4. Close cache connections
	if a.Cache != nil {
		setErr(a.Cache.Close())
	}

	// 5. Close database connections
	if a.DB != nil {
		setErr(a.DB.Close())
		orm.ResetDefault()
	}

	// 6. Shutdown chain providers in reverse order (before WithProviders providers)
	for i := len(a.chainProviders) - 1; i >= 0; i-- {
		setErr(a.chainProviders[i].Shutdown(ctx))
	}

	// 7. Shutdown WithProviders providers in reverse registration order
	for i := len(a.providers) - 1; i >= 0; i-- {
		setErr(a.providers[i].Shutdown(ctx))
	}

	// 8. Close logger if it supports it (e.g., file logger) — last, so all
	// prior shutdown steps can still log.
	if a.Log != nil {
		if closer, ok := a.Log.(interface{ Close() error }); ok {
			setErr(closer.Close())
		}
	}

	if firstErr != nil {
		return fmt.Errorf("velocity: shutdown error: %w", firstErr)
	}

	return nil
}

// Version returns the framework version.
func (a *App) Version() string {
	return a.version
}

// Run starts the application.
func (a *App) Run() {
	fmt.Printf("Velocity v%s is running! (Local development mode)\n", a.version)
}

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
func (a *App) Schedule(fn func(*scheduler.Scheduler)) *App {
	a.scheduleFn = fn
	return a
}

// Exceptions registers a callback that configures the exception handler.
func (a *App) Exceptions(fn func(*exceptions.Handler)) *App {
	a.exceptionsFn = fn
	return a
}

// bootstrap orchestrates the declarative chain in a fixed order.
// It is called once at the start of Serve(). Safe to call multiple times
// (guarded by bootstrapped flag).
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

	// 2. Re-wire instance events (idempotent — safe to call again)
	wireInstanceEvents(a)

	// 3. Build middleware stack
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

	// 4. Register routes
	routing := &Routing{router: a.Router, middleware: mwStack}

	dispatchProviderCallback(a.chainProviders, func(rp RouteProvider) {
		rp.Routes(routing)
	})
	if a.routesFn != nil {
		a.routesFn(routing)
	}

	// 5. Register events
	dispatchProviderCallback(a.chainProviders, func(ep EventProvider) {
		ep.Events(a.Services.Events)
	})
	if a.eventsFn != nil {
		a.eventsFn(a.Services.Events)
	}

	// 6. Register scheduled jobs
	dispatchProviderCallback(a.chainProviders, func(sp ScheduleProvider) {
		sp.Schedule(a.Services.Scheduler)
	})
	if a.scheduleFn != nil {
		a.scheduleFn(a.Services.Scheduler)
	}

	// 7. Configure exceptions
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

	a.Router.SetInstanceEventDispatcher(dispatch)

	if a.DB != nil {
		a.DB.SetEventDispatcher(dispatch)
	}
	if a.Cache != nil {
		a.Cache.SetEventDispatcher(dispatch)
	}
	if a.Notification != nil {
		a.Notification.SetEventDispatcher(dispatch)
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

// --- Service initializers ---

func initCache(config CacheConfig) *cache.Manager {
	cacheConfig := &cache.Config{
		Default: "default",
		Prefix:  config.Prefix,
		Stores:  make(map[string]cache.StoreConfig),
	}

	switch config.Driver {
	case "file":
		cacheConfig.Stores["default"] = cache.StoreConfig{
			Driver: "file",
			Path:   config.Path,
		}
	case "redis":
		cacheConfig.Stores["default"] = cache.StoreConfig{
			Driver:   "redis",
			Host:     config.RedisHost,
			Port:     config.RedisPort,
			Password: config.RedisPassword,
			Database: config.RedisDatabase,
		}
	default:
		cacheConfig.Stores["default"] = cache.StoreConfig{
			Driver: "memory",
		}
	}

	return cache.NewManager(cacheConfig)
}

func initStorage(config StorageConfig, logger log.Logger) *storage.Manager {
	storageCfg := storage.Config{
		Default: config.Default,
		Disks:   make(map[string]storage.DiskConfig),
	}
	for name, disk := range config.Disks {
		storageCfg.Disks[name] = storage.DiskConfig{
			Driver:     disk.Driver,
			Root:       disk.Root,
			URL:        disk.URL,
			Visibility: disk.Visibility,
			Key:        disk.Key,
			Secret:     disk.Secret,
			Region:     disk.Region,
			Bucket:     disk.Bucket,
			MaxSize:    disk.MaxSize,
		}
	}
	mgr := storage.NewManager(storageCfg)
	if err := mgr.Configure(storageCfg); err != nil {
		if logger != nil {
			logger.Warn("Failed to configure storage disks", "error", err)
		}
	}
	return mgr
}

func initAuth(authCfg AuthConfig, sessCfg SessionConfig, logger log.Logger, db *sql.DB, enc crypto.Encryptor) *auth.Manager {
	manager := auth.NewManager()

	if authCfg.DefaultGuard != "" {
		manager.SetDefaultGuard(authCfg.DefaultGuard)
	}
	if authCfg.BcryptCost > 0 {
		manager.SetHasher(auth.NewBcryptHasher(authCfg.BcryptCost))
	}

	// Register providers
	for name, provCfg := range authCfg.Providers {
		switch provCfg.Driver {
		case "orm":
			model := provCfg.Model
			if model == "" {
				model = "User"
			}
			manager.RegisterProvider(name, auth.NewORMUserProvider(db, model, manager.GetHasher()))
		}
	}

	// Register guards
	for name, guardCfg := range authCfg.Guards {
		provider, err := manager.Provider(guardCfg.Provider)
		if err != nil {
			if logger != nil {
				logger.Warn("Auth guard skipped: provider not found", "guard", name, "provider", guardCfg.Provider)
			}
			continue
		}

		switch guardCfg.Driver {
		case "session":
			guard, err := guards.NewSessionGuard(provider, sessCfg, enc)
			if err != nil {
				if logger != nil {
					logger.Warn("Failed to create session guard", "guard", name, "error", err)
				}
				continue
			}
			manager.RegisterGuard(name, guard)

		case "jwt":
			var jwtCfg auth.JWTConfig
			if opts, ok := guardCfg.Options["jwt"]; ok {
				if jc, ok := opts.(auth.JWTConfig); ok {
					jwtCfg = jc
				} else if logger != nil {
					logger.Warn("JWT guard config has wrong type, using defaults", "guard", name, "type", fmt.Sprintf("%T", opts))
				}
			}
			if jwtCfg.Secret == "" {
				if logger != nil {
					logger.Warn("JWT guard skipped: no secret configured", "guard", name)
				}
				continue
			}
			guard := guards.NewJWTGuard(provider, jwtCfg)
			manager.RegisterGuard(name, guard)
		}
	}

	return manager
}

func initQueue(config QueueConfig, db *sql.DB) queue.Driver {
	switch config.Driver {
	case "redis":
		d, err := queue.NewRedisDriver(queue.RedisConfig{
			Host:     config.RedisHost,
			Port:     config.RedisPort,
			Password: config.RedisPassword,
			DB:       config.RedisDB,
		})
		if err != nil {
			return queue.NewMemoryDriver()
		}
		return d
	case "database":
		if db == nil {
			return queue.NewMemoryDriver()
		}
		return queue.NewDatabaseDriver(db)
	default:
		return queue.NewMemoryDriver()
	}
}

func initNotification(mailer mail.Mailer, db *sql.DB, dbDriver string) *notification.Manager {
	mgr := notification.NewManager()

	// Wire the mail channel with the framework's mailer
	if mailer != nil {
		if ch, err := mgr.Channel("mail"); err == nil {
			if mc, ok := ch.(*channels.MailChannel); ok {
				mc.SetMailer(mailer)
			}
		}
	}

	// Wire the database channel with the framework's DB
	if db != nil {
		if ch, err := mgr.Channel("database"); err == nil {
			if dc, ok := ch.(*channels.DatabaseChannel); ok {
				dc.SetDB(db, dbDriver)
			}
		}
	}

	return mgr
}

// runProviderLifecycle runs the Register and Boot phases on a slice of providers.
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

// dispatchProviderCallback iterates providers and calls fn for those that implement T.
func dispatchProviderCallback[T any](providers []app.ServiceProvider, fn func(T)) {
	for _, p := range providers {
		if t, ok := any(p).(T); ok {
			fn(t)
		}
	}
}

// NewTestApp creates an App with in-memory services suitable for testing.
// Uses memory cache, memory queue, and console logger.
func NewTestApp(opts ...Option) (*App, error) {
	config := Config{
		Name:  "Velocity Test",
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{
			Driver: "memory",
			Prefix: "test_cache",
		},
		Log: LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{
			Driver: "memory",
		},
		Mail: MailConfig{
			Driver: "log",
		},
	}

	allOpts := []Option{WithConfig(config)}
	allOpts = append(allOpts, opts...)
	return New(allOpts...)
}
