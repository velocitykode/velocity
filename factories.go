package velocity

import (
	"database/sql"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/auth/stores/ormauth"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/notification"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/storage"
)

// initCache builds the cache manager from config. A zero config (no
// stores) gets one memory store named "default", so an app built with
// WithConfig and no cache settings still has a working cache.
// The manager takes logger at once, so a store built during New (the
// server session store's) writes its startup warnings through it.
func initCache(config cache.Config, logger contract.Logger) *cache.Manager {
	if len(config.Stores) == 0 {
		config = cache.SingleStoreConfig(config.Prefix, cache.StoreConfig{Driver: cache.DriverMemory})
	}
	m := cache.NewManager(&config)
	m.SetLogger(logger)
	return m
}

// initStorage builds the storage manager and configures its disks. Disk
// configuration failures are logged as warnings rather than failing
// startup, since storage may not be required by every app.
func initStorage(config storage.Config, logger contract.Logger) *storage.Manager {
	mgr := storage.NewManager(config)
	if err := mgr.Configure(config); err != nil {
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
//	s.Auth.(*auth.Manager).SetUserStore(ormauth.New[models.Admin]())
//
// SetUserStore re-points every registered scheme, so the swap works regardless
// of whether it runs before or after this function.
//
// Misconfigured schemes are skipped with a warning so the app can still start -
// only the broken scheme is unavailable at runtime.
func initAuth(authCfg auth.Config, logger contract.Logger, enc crypto.Encryptor, sessionOpts ...schemes.SessionSchemeOption) *auth.Manager {
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
			scheme, err := schemes.NewSessionScheme(userStore, authCfg.Session, enc, sessionOpts...)
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
					logger.Warn("JWT scheme config has wrong type, using defaults", "scheme", name, "type", errchain.Sprintf("%T", opts))
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

// initDB creates the ORM manager from config, writing through logger from
// the first statement its driver runs. Returns nil if no connection is
// configured.
func initDB(config orm.Config, logger contract.Logger) (*orm.Manager, error) {
	if config.Driver == "" {
		return nil, nil
	}
	config.Logger = logger
	return orm.NewManager(config)
}

// initQueue selects the queue driver based on config, delegating to the
// canonical queue.NewQueue resolver so the driver registry is the single
// integration point.
//
// Returns an error when payload-signing setup, Redis connect, or a missing
// DB for the database driver prevents the requested driver from starting,
// so boot fails loudly rather than silently downgrading to the in-memory
// driver.
func initQueue(config queue.Config, db *sql.DB, dbDriver string, appKey string, appEnv string, encryptor contract.Encryptor, logger contract.Logger) (_ queue.Driver, err error) {
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
	if err := queue.ConfigureSigningWith(config.SigningKey, appKey, queue.SigningOptions{
		AcceptUnsigned:     config.AcceptUnsigned,
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

	config.DB = db
	config.DBDriver = dbDriver
	// The driver logs through the framework logger from construction
	// on (the redis driver's startup warnings included); NewQueue
	// installs it on a driver implementing contract.LoggerAware.
	config.Logger = logger
	d, err := queue.NewQueue(config)
	if err != nil {
		return nil, err
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
			return nil, errchain.Errorf("velocity/queue: failed to wire database batch repository: %w", repoErr)
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
