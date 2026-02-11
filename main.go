package velocity

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/velocitykode/velocity/pkg/auth"
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/crypto"
	"github.com/velocitykode/velocity/pkg/csrf"
	"github.com/velocitykode/velocity/pkg/events"
	"github.com/velocitykode/velocity/pkg/log"
	"github.com/velocitykode/velocity/pkg/mail"
	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/queue"
	"github.com/velocitykode/velocity/pkg/router"
	"github.com/velocitykode/velocity/pkg/scheduler"
	"github.com/velocitykode/velocity/pkg/storage"
	"github.com/velocitykode/velocity/pkg/view"
)

const frameworkVersion = "0.1.0"

// App represents the Velocity application container.
// It owns all framework subsystem instances and provides them to the consumer.
type App struct {
	// Core services — all exported for direct access
	Router    *router.VelocityRouterV2
	DB        *orm.Manager
	Auth      *auth.Manager
	Log       log.Logger
	Cache     *cache.Manager
	Crypto    crypto.Encryptor
	CSRF      *csrf.CSRF
	Events    events.Dispatcher
	Queue     queue.Driver
	Storage   *storage.Manager
	View      *view.Engine
	Scheduler *scheduler.Scheduler
	Mail      mail.Mailer

	// Internal
	config  *Config
	server  *http.Server
	version string
}

// New creates a new Velocity application with all services initialized.
// Services are initialized in dependency order. If any required service
// fails to initialize, New returns an error — it never panics.
func New(opts ...Option) (*App, error) {
	app := &App{
		version: frameworkVersion,
	}

	// Load config from env by default
	config := ConfigFromEnv()
	app.config = &config

	// Apply options (may override config)
	for _, opt := range opts {
		opt(app)
	}

	// 1. Initialize logger first (everything else may need to log)
	logger, err := log.NewLogger(log.LogConfig{
		Driver: app.config.Log.Driver,
		Config: app.config.Log.Config,
	})
	if err != nil {
		// Fall back to console logger
		logger, _ = log.NewLogger(log.LogConfig{Driver: "console"})
	}
	app.Log = logger

	// 2. Initialize crypto (auth/csrf may need it)
	if app.config.Crypto.Key != "" {
		enc, err := crypto.NewEncryptor(crypto.Config{
			Key:          app.config.Crypto.Key,
			PreviousKeys: app.config.Crypto.PreviousKeys,
			Cipher:       app.config.Crypto.Cipher,
		})
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize crypto: %w", err)
		}
		app.Crypto = enc
	}

	// 3. Initialize database connection
	if app.config.DB.Connection != "" {
		dbManager, err := orm.NewManager(orm.ManagerConfig{
			Driver:          app.config.DB.Connection,
			Host:            app.config.DB.Host,
			Port:            app.config.DB.Port,
			Database:        app.config.DB.Database,
			Username:        app.config.DB.Username,
			Password:        app.config.DB.Password,
			Charset:         app.config.DB.Charset,
			SSLMode:         app.config.DB.SSLMode,
			MaxIdleConns:    app.config.DB.MaxIdleConns,
			MaxOpenConns:    app.config.DB.MaxOpenConns,
			ConnMaxLifetime: app.config.DB.ConnMaxLifetime,
			LogQueries:      app.config.DB.LogQueries,
			SlowThreshold:   app.config.DB.SlowThreshold,
		})
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize database: %w", err)
		}
		app.DB = dbManager
	}

	// 4. Initialize auth manager
	app.Auth = auth.NewManager()

	// 5. Initialize cache
	app.Cache = initCache(app.config.Cache)

	// 6. Initialize CSRF
	app.CSRF = csrf.New(nil)

	// 7. Initialize view/bond engine
	if app.config.View.RootTemplate != "" {
		viewEngine, err := view.NewEngine(view.Config{
			RootTemplate: app.config.View.RootTemplate,
			Version:      app.config.View.Version,
			SSREnabled:   app.config.View.SSREnabled,
			SSRURL:       app.config.View.SSRURL,
		})
		if err != nil {
			return nil, fmt.Errorf("velocity: failed to initialize view engine: %w", err)
		}
		app.View = viewEngine
	}

	// 8. Initialize events dispatcher
	app.Events = events.NewDispatcher()

	// 9. Initialize queue
	app.Queue = initQueue(app.config.Queue)

	// 10. Initialize storage
	app.Storage = storage.NewManager(storage.Config{
		Default: app.config.Storage.Default,
	})

	// 11. Initialize scheduler
	app.Scheduler = scheduler.New()

	// 12. Initialize mail
	if app.config.Mail.Driver != "" {
		mailer, err := mail.NewMailer(mail.MailConfig{
			Driver: app.config.Mail.Driver,
		})
		if err != nil {
			app.Log.Warn("Failed to initialize mailer", "error", err)
		} else {
			app.Mail = mailer
		}
	}

	// 13. Create router
	app.Router = router.New()

	// Wire event dispatching into packages
	wireEvents(app)

	return app, nil
}

// Default creates a pre-configured App from env vars and sets it as the
// default for all package-level convenience functions. This provides a
// migration path from global singletons to explicit DI.
func Default(opts ...Option) (*App, error) {
	app, err := New(opts...)
	if err != nil {
		return nil, err
	}
	setDefaultApp(app)
	return app, nil
}

// Serve starts the HTTP server with signal handling and graceful shutdown.
func (a *App) Serve() error {
	addr := ":" + a.config.Port
	a.server = &http.Server{
		Addr:    addr,
		Handler: a.Router,
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

	// 3. Close cache connections
	if a.Cache != nil {
		setErr(a.Cache.Close())
	}

	// 4. Close database connections
	if a.DB != nil {
		setErr(a.DB.Close())
	}

	if firstErr != nil {
		return fmt.Errorf("velocity: shutdown error: %w", firstErr)
	}

	a.Log.Info("Velocity server stopped")
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

// setDefaultApp wires the App's service instances into the package-level globals
// for backward compatibility with code that uses package-level convenience functions.
func setDefaultApp(app *App) {
	// Wire router global
	if app.Router != nil {
		router.SetGlobalRouter(app.Router)
	}

	// Wire logger global
	if app.Log != nil {
		log.SetGlobalLogger(app.Log)
	}

	// Wire crypto global
	if app.Crypto != nil {
		crypto.Init(crypto.Config{
			Key:          app.config.Crypto.Key,
			PreviousKeys: app.config.Crypto.PreviousKeys,
			Cipher:       app.config.Crypto.Cipher,
		})
	}

	// Wire auth global
	if app.Auth != nil {
		auth.SetGlobalManager(app.Auth)
	}

	// Wire events global
	if app.Events != nil {
		events.Initialize(app.Events)
	}

	// Wire CSRF global
	if app.CSRF != nil {
		csrf.SetGlobalCSRF(app.CSRF)
	}

	// Wire view/bond global
	if app.View != nil {
		view.SetGlobalEngine(app.View)
	}

	// Wire queue global
	if app.Queue != nil {
		queue.SetDefault(app.Queue)
	}

	// Wire storage global
	if app.Storage != nil {
		storage.SetGlobalManager(app.Storage)
	}

	// Wire mail global
	if app.Mail != nil {
		mail.SetDefaultMailer(app.Mail)
	}
}

// wireEvents wires the App's event dispatcher into subsystem packages
// using the instance-based hook from the events package.
func wireEvents(app *App) {
	if app.Events == nil {
		return
	}
	events.WirePackageHooks(app.Events)
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

func initQueue(config QueueConfig) queue.Driver {
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
		return queue.NewDatabaseDriver()
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
