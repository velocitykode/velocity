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

	"github.com/velocitykode/velocity/pkg/app"
	"github.com/velocitykode/velocity/pkg/auth"
	"github.com/velocitykode/velocity/pkg/auth/drivers/guards"
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/crypto"
	"github.com/velocitykode/velocity/pkg/csrf"
	"github.com/velocitykode/velocity/pkg/events"
	"github.com/velocitykode/velocity/pkg/exceptions"
	"github.com/velocitykode/velocity/pkg/log"
	"github.com/velocitykode/velocity/pkg/mail"
	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/queue"
	"github.com/velocitykode/velocity/pkg/router"
	"github.com/velocitykode/velocity/pkg/scheduler"
	"github.com/velocitykode/velocity/pkg/storage"
	"github.com/velocitykode/velocity/pkg/validation"
	"github.com/velocitykode/velocity/pkg/view"
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
	config  *Config
	server  *http.Server
	version string
}

// New creates a new Velocity application with all services initialized.
// Services are initialized in dependency order. If any required service
// fails to initialize, New returns an error — it never panics.
func New(opts ...Option) (*App, error) {
	a := &App{
		Services: &app.Services{},
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
	a.Events = events.NewDispatcher()

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

	// 13. Create router and inject services
	a.Router = router.New()
	a.Router.SetServices(a.Services)

	// 14. Initialize exception handler
	a.Exceptions = exceptions.NewHandler(
		exceptions.WithDebug(a.config.Debug),
		exceptions.WithEnvironment(a.config.Env),
	)

	// 15. Initialize validator
	a.Validator = validation.NewValidator()

	// Wire event dispatchers into service instances
	wireInstanceEvents(a)

	return a, nil
}

// Serve starts the HTTP server with signal handling and graceful shutdown.
func (a *App) Serve() error {
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

	// 6. Close logger if it supports it (e.g., file logger)
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

// wireInstanceEvents wires the event dispatcher into service instances.
// Each service that fires events gets the dispatcher set on its instance.
func wireInstanceEvents(a *App) {
	if a.Events == nil {
		return
	}

	dispatch := func(event interface{}) error {
		return a.Events.Dispatch(event)
	}

	a.Router.SetInstanceEventDispatcher(dispatch)

	if a.DB != nil {
		a.DB.SetEventDispatcher(dispatch)
	}
	if a.Cache != nil {
		a.Cache.SetEventDispatcher(dispatch)
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
