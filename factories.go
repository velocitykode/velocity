package velocity

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
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
			Driver:        "file",
			Path:          config.Path,
			MaxValueBytes: config.MaxValueBytes,
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
			Driver:        "memory",
			MaxEntries:    config.MemoryMaxEntries,
			MaxValueBytes: config.MaxValueBytes,
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

// initAuth builds the auth manager: one ORM-backed user store plus the
// configured schemes (session, JWT).
//
// Velocity authenticates a single identity store. The user store installed here
// is the framework default, backed by ormauth.User against the users table;
// an application swaps in its own model from a module:
//
//	s.Auth.SetUserStore(ormauth.New[models.Admin]())
//
// SetUserStore re-points every registered scheme, so the swap works regardless
// of whether it runs before or after this function.
//
// Misconfigured schemes are skipped with a warning so the app can still start -
// only the broken scheme is unavailable at runtime.
func initAuth(authCfg auth.Config, sessCfg auth.SessionConfig, logger log.Logger, enc crypto.Encryptor) *auth.Manager {
	manager := auth.NewManager()

	// Route auth diagnostics (authentication/authorization denials, hasher
	// warnings) through the framework logger. Safe to pass nil.
	manager.SetLogger(logger)

	if authCfg.DefaultScheme != "" {
		manager.SetDefaultScheme(authCfg.DefaultScheme)
	}
	if authCfg.BcryptCost > 0 {
		manager.SetHasher(auth.NewBcryptHasher(authCfg.BcryptCost))
	}

	// Parse the trusted-proxy list and propagate it to every scheme via
	// the auth.TrustedProxiesReceiver interface. SetTrustedProxies must
	// be called BEFORE RegisterScheme below so newly registered schemes
	// inherit the list at registration time. A malformed entry is
	// logged and the list is dropped (no proxies trusted), which is
	// the same "warn-and-continue" stance the rest of initAuth uses
	// for scheme-level misconfiguration. Operators who want fail-fast
	// should validate at boot via clientip.ParseCIDRs explicitly.
	if len(authCfg.TrustedProxies) > 0 {
		proxies, err := clientip.ParseCIDRs(authCfg.TrustedProxies)
		if err != nil {
			if logger != nil {
				logger.Warn("Auth trusted proxies parse failed; ignoring list (XFF headers will be untrusted)", "error", err)
			}
		} else {
			// Pass a deep clone so this function's local `proxies`
			// reference, retained by anything caller-side after
			// initAuth returns, cannot influence the manager's
			// trust decisions at runtime. SetTrustedProxies also
			// deep-clones on its write path as belt-and-braces.
			manager.SetTrustedProxies(clientip.CloneIPNets(proxies))
		}
	}

	// Install the framework's default user store. It queries through the
	// ORM's generic builder, so the table name, placeholder dialect, and
	// identifier quoting all come from the grammar rather than from SQL
	// assembled here.
	manager.SetUserStore(ormauth.New[ormauth.User](ormauth.WithHasher(manager.GetHasher())))
	userStore := manager.DefaultUserStore()

	// Register schemes
	for name, schemeCfg := range authCfg.Schemes {
		switch schemeCfg.Driver {
		case "session":
			scheme, err := schemes.NewSessionScheme(userStore, sessCfg, enc)
			if err != nil {
				if logger != nil {
					logger.Warn("Failed to create session scheme", "scheme", name, "error", err)
				}
				continue
			}
			// Propagate the operator-configured bcrypt cost so the
			// dummy-hash timing defense (H-09) runs at the same cost
			// as the real verify. Without this the missing-user
			// path runs cost 10 while real verify runs cost 14,
			// reopening the username-enumeration channel (F2).
			if authCfg.BcryptCost > 0 {
				scheme.SetHasher(auth.NewBcryptHasher(authCfg.BcryptCost))
			}
			if authCfg.AttemptFloor != 0 {
				scheme.SetAttemptFloor(authCfg.AttemptFloor)
			}
			manager.RegisterScheme(name, scheme)

		case "jwt":
			var jwtCfg auth.JWTConfig
			if opts, ok := schemeCfg.Options["jwt"]; ok {
				if jc, ok := opts.(auth.JWTConfig); ok {
					jwtCfg = jc
				} else if logger != nil {
					logger.Warn("JWT scheme config has wrong type, using defaults", "scheme", name, "type", fmt.Sprintf("%T", opts))
				}
			}
			if jwtCfg.Secret == "" {
				if logger != nil {
					logger.Warn("JWT scheme skipped: no secret configured", "scheme", name)
				}
				continue
			}
			scheme, err := schemes.NewJWTScheme(userStore, jwtCfg)
			if err != nil {
				if logger != nil {
					logger.Warn("Failed to create JWT scheme", "scheme", name, "error", err)
				}
				continue
			}
			if authCfg.BcryptCost > 0 {
				scheme.SetHasher(auth.NewBcryptHasher(authCfg.BcryptCost))
			}
			if authCfg.AttemptFloor != 0 {
				scheme.SetAttemptFloor(authCfg.AttemptFloor)
			}
			scheme.Start()
			manager.RegisterScheme(name, scheme)
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
		TimeZone:        config.TimeZone,
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
func initQueue(config QueueConfig, db *sql.DB, dbDriver string, signingKey string, appKey string, appEnv string, encryptor contract.Encryptor, logger log.Logger) (_ queue.Driver, err error) {
	// Route queue-signing diagnostics through the framework logger before
	// configuring so missing/APP_KEY fallbacks are surfaced consistently.
	queue.SetSigningLogger(logger)

	// The signing logger above and the payload encryptor below are
	// process-global hooks installed before the fallible driver/repo
	// construction steps. The caller's cleanup stack only learns about
	// them once initQueue returns successfully, so on any error roll
	// them back here; otherwise a failed New() would leave hooks
	// retaining the torn-down app's logger/encryptor.
	defer func() {
		if err != nil {
			queue.SetSigningLogger(nil)
			queue.SetPayloadEncryptor(nil)
		}
	}()

	// Configure payload signing now that .env has been loaded. This used
	// to run in queue's package init(), which fired before godotenv.Load
	// had populated APP_KEY/QUEUE_SIGNING_KEY, so signing was always
	// reported as disabled even when the key was present.
	//
	// Fail-closed: when neither QUEUE_SIGNING_KEY nor APP_KEY is set and
	// the process is not running under a dev/test profile,
	// ConfigureSigningWith returns ErrSigningKeyRequired so boot stops.
	// Operators who explicitly want to run unsigned (migration window,
	// local dev queue) opt in via QUEUE_ACCEPT_UNSIGNED=true; the warning
	// log path stays so the choice is visible in startup logs.
	if err := queue.ConfigureSigningWith(signingKey, appKey, queue.SigningOptions{
		AcceptUnsigned:     queueAcceptUnsigned(),
		AllowUnsignedInDev: app.IsDevOrTestEnv(appEnv),
	}); err != nil {
		return nil, err
	}

	// Opt-in payload encryption (QUEUE_ENCRYPT=true): seal Payload.Data at
	// rest with the app encryptor. Fail-closed when the operator asked for
	// encryption but the crypto subsystem is unavailable (APP_KEY unset);
	// silently proceeding would persist plaintext the operator believes is
	// encrypted. The else branch always clears the package state so a
	// previous app instance in the same process (tests, embed mode) cannot
	// leak its encryptor into this one.
	if config.Encrypt {
		if encryptor == nil {
			return nil, queue.ErrEncryptorRequired
		}
		queue.SetPayloadEncryptor(encryptor)
	} else {
		queue.SetPayloadEncryptor(nil)
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

	// C-03 follow-up: when the operator picks the database queue driver,
	// the batch state needs to live in shared storage too. Without this
	// auto-install the framework would silently fall back to the in-memory
	// batch repository even on a multi-host install, so worker counters
	// and Cancel state would not cross process boundaries (which was the
	// original C-03 defect).
	//
	// Idempotent: EnsureDefaultBatchRepository is a no-op when the app
	// has already wired a custom repo via SetDefaultBatchRepository, so
	// users who want a different storage (e.g. a Redis-backed repo for
	// a Redis queue) are not overwritten.
	if _, ok := d.(*queue.DatabaseDriver); ok && db != nil {
		repo, repoErr := queue.NewDatabaseBatchRepository(db, dbDriver)
		if repoErr != nil {
			// Surface the misconfiguration loudly at boot rather than
			// silently dropping back to the in-memory default. The
			// queue driver itself accepts dbDriver values that the
			// batch repo rejects, so this catches typos that would
			// otherwise hide for weeks.
			return nil, fmt.Errorf("velocity/queue: failed to wire database batch repository: %w", repoErr)
		}
		if !queue.EnsureDefaultBatchRepository(repo) {
			logger.Info("Skipping DatabaseBatchRepository auto-install: a custom batch repository was already configured")
		}
	}

	// C-03-fb2 HIGH 1: wire the queue driver for cross-process callback
	// delivery so Then/Catch/Finally callbacks registered by name (via
	// PendingBatch.OnComplete / OnFailed / OnFinally) can be executed by
	// a worker on ANY host when the terminal completion CAS fires. The
	// driver is consulted lazily so the dispatcher's BatchCallbackJob
	// enqueue picks up whichever driver is currently wired, not a stale
	// snapshot from boot.
	queue.SetBatchCallbackQueue(d, "default")

	return d, nil
}

// initNotification creates the notification manager and wires the mail and database
// channels with their dependencies. Channels whose dependencies are nil (no mailer
// configured, no DB connection) are silently left unwired - they'll error at send time.
func initNotification(mailer mail.Mailer, db *sql.DB, dbDriver string) *notification.Manager {
	mgr := notification.NewManager()

	// Wire the mail channel with the framework's mailer
	if mailer != nil {
		if ch, err := mgr.Channel("mail"); err == nil {
			if mc, ok := ch.(notification.MailerAware); ok {
				mc.SetMailer(mailer)
			}
		}
	}

	// Wire the database channel with the framework's DB
	if db != nil {
		if ch, err := mgr.Channel("database"); err == nil {
			if dc, ok := ch.(notification.DBAware); ok {
				dc.SetDB(db, dbDriver)
			}
		}
	}

	return mgr
}

// queueAcceptUnsigned reports whether the operator has explicitly opted
// into running the queue without payload signing. Recognises the common
// truthy spellings so a typo does not silently disable the scheme.
//
// final: do not rename. QUEUE_ACCEPT_UNSIGNED is the 1.0 surface name for
// the queue payload-signing opt-out.
func queueAcceptUnsigned() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("QUEUE_ACCEPT_UNSIGNED"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
