package velocity

import (
	"database/sql"
	"fmt"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/notification/channels"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/storage"
)

// initCache translates the flat CacheConfig (single driver) into the multi-store
// cache.Config the cache package expects. Defaults to memory when driver is unrecognized.
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
			TLS:      config.RedisTLS,
		}
	default:
		cacheConfig.Stores["default"] = cache.StoreConfig{
			Driver: "memory",
		}
	}

	return cache.NewManager(cacheConfig)
}

// initStorage maps each DiskConfig entry into the storage package's format and
// configures drivers. Disk configuration failures are logged as warnings rather
// than failing startup, since storage may not be required by every app.
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

// initAuth builds the auth manager by registering user providers (ORM-backed) and
// guards (session, JWT) from config. Misconfigured guards are skipped with a warning
// so the app can still start — only the broken guard is unavailable at runtime.
func initAuth(authCfg auth.Config, sessCfg auth.SessionConfig, logger log.Logger, db *sql.DB, enc crypto.Encryptor) *auth.Manager {
	manager := auth.NewManager()

	// Route auth diagnostics (authentication/authorization denials, hasher
	// warnings) through the framework logger. Safe to pass nil.
	manager.SetLogger(logger)

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
			guard, err := guards.NewJWTGuard(provider, jwtCfg)
			if err != nil {
				if logger != nil {
					logger.Warn("Failed to create JWT guard", "guard", name, "error", err)
				}
				continue
			}
			guard.Start()
			manager.RegisterGuard(name, guard)
		}
	}

	return manager
}

// initDB creates the ORM manager from config. Returns nil if no connection is configured.
func initDB(config DBConfig) (*orm.Manager, error) {
	if config.Connection == "" {
		return nil, nil
	}
	return orm.NewManager(orm.ManagerConfig{
		Driver:          config.Connection,
		Host:            config.Host,
		Port:            config.Port,
		Database:        config.Database,
		Username:        config.Username,
		Password:        config.Password,
		Charset:         config.Charset,
		SSLMode:         config.SSLMode,
		TLS:             config.TLS,
		MaxIdleConns:    config.MaxIdleConns,
		MaxOpenConns:    config.MaxOpenConns,
		ConnMaxLifetime: config.ConnMaxLifetime,
		LogQueries:      config.LogQueries,
		SlowThreshold:   config.SlowThreshold,
	})
}

// initQueue selects the queue driver based on config, delegating to the
// canonical queue.NewQueue resolver so the driver registry is the single
// integration point.
//
// Returns an error when payload-signing setup, Redis connect, or a missing
// DB for the database driver prevents the requested driver from starting,
// so boot fails loudly rather than silently downgrading to the in-memory
// driver.
func initQueue(config QueueConfig, db *sql.DB, dbDriver string, signingKey string, appKey string, logger log.Logger) (queue.Driver, error) {
	// Route queue-signing diagnostics through the framework logger before
	// configuring so missing/APP_KEY fallbacks are surfaced consistently.
	queue.SetSigningLogger(logger)

	// Configure payload signing now that .env has been loaded. This used
	// to run in queue's package init(), which fired before godotenv.Load
	// had populated APP_KEY/QUEUE_SIGNING_KEY, so signing was always
	// reported as disabled even when the key was present.
	if err := queue.ConfigureSigning(signingKey, appKey); err != nil {
		return nil, err
	}

	d, err := queue.NewQueue(queue.QueueConfig{
		Driver: config.Driver,
		Redis: queue.RedisConfig{
			Host:     config.RedisHost,
			Port:     config.RedisPort,
			Password: config.RedisPassword,
			DB:       config.RedisDB,
			TLS:      config.RedisTLS,
		},
		DB:       db,
		DBDriver: dbDriver,
	})
	if err != nil {
		return nil, err
	}

	// The memory driver wants a logger so worker diagnostics route through
	// the framework log; setting it here keeps the registry factories
	// dependency-free.
	if mem, ok := d.(*queue.MemoryDriver); ok {
		mem.SetLogger(logger)
	}
	return d, nil
}

// initNotification creates the notification manager and wires the mail and database
// channels with their dependencies. Channels whose dependencies are nil (no mailer
// configured, no DB connection) are silently left unwired — they'll error at send time.
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
