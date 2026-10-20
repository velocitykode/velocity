package queue

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
	"github.com/velocitykode/velocity/internal/errchain"
)

// Config holds configuration for creating a queue driver.
type Config struct {
	// AcceptUnsigned opts into running without any signing key outside
	// development and testing. It is a process-wide setting: velocity.New
	// applies it through ConfigureSigningWith before it calls NewQueue,
	// which does not read it; a standalone caller applies it the same way
	// (QUEUE_ACCEPT_UNSIGNED, default false).
	AcceptUnsigned bool

	// DB holds a *sql.DB for the database driver. Required when Driver is
	// "database". velocity.New sets it from the app's database, replacing
	// any value in velocity.Config.
	DB *sql.DB

	// DBDriver specifies the database driver name ("postgres", "mysql",
	// "sqlite"). Required when Driver is "database". velocity.New sets it
	// from the app's database, replacing any value in velocity.Config.
	DBDriver string

	// Driver is the queue driver to use: "memory", "redis", or "database"
	// (QUEUE_DRIVER, default "memory").
	Driver string

	// Encrypt seals job payload data at rest with the app encryptor. It is
	// a process-wide setting: velocity.New applies it through
	// SetPayloadEncryptor before it calls NewQueue, which does not read
	// it; a standalone caller applies it the same way (QUEUE_ENCRYPT,
	// default false).
	Encrypt bool

	// Logger receives the driver's warnings, from construction on: a
	// driver factory may write startup warnings through it, and NewQueue
	// installs it on a driver implementing contract.LoggerAware. Nil leaves
	// the driver on the framework's standalone fallback logger.
	// velocity.New sets it to the app logger, replacing any value in
	// velocity.Config.
	Logger contract.Logger

	// Redis holds Redis-specific configuration. Required when Driver is
	// "redis".
	Redis RedisConfig

	// SigningKey is the HMAC key for payload signing (SENSITIVE: do not
	// log). Empty falls back to the app key. It is a process-wide setting:
	// velocity.New applies it through ConfigureSigningWith before it calls
	// NewQueue, which does not read it; a standalone caller applies it the
	// same way (QUEUE_SIGNING_KEY, default empty).
	SigningKey string
}

// String returns a safe representation with the signing key and the Redis
// password redacted, so a driver factory that logs its config with %v does
// not leak them.
func (c Config) String() string {
	key := ""
	if c.SigningKey != "" {
		key = "[REDACTED]"
	}
	return fmt.Sprintf("queue.Config{Driver:%s, Redis:%s, DBDriver:%s, SigningKey:%s, AcceptUnsigned:%t, Encrypt:%t}",
		c.Driver, c.Redis, c.DBDriver, key, c.AcceptUnsigned, c.Encrypt)
}

// GoString keeps %#v redacted too.
func (c Config) GoString() string { return c.String() }

// Validate checks that Driver names a registered queue driver (empty is
// accepted: NewQueue treats it as "memory"), so an unknown or unimported
// driver fails at boot. It checks only what the caller configures: the
// database driver's DB and DBDriver are wired by velocity.New and checked
// by that driver's factory.
func (c Config) Validate() error {
	if c.Driver == "" {
		return nil
	}
	return drivers.Check(c.Driver)
}

// drivers is the canonical Velocity driver registry for queues. Built-in
// drivers (memory, redis, database) self-register from this file's init();
// third-party queue backends can register additional factories.
var drivers = driverregistry.New[Driver, Config]("queue")

// Drivers returns the registry that queue drivers register themselves
// into. Use this from a driver package's init() to install a factory:
//
//	func init() {
//	    queue.Drivers().Register("kafka", func(ctx context.Context, cfg queue.Config) (queue.Driver, error) {
//	        return newKafkaDriver(cfg), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Driver, Config] { return drivers }

func init() {
	Drivers().Register("memory", func(_ context.Context, _ Config) (Driver, error) {
		d := NewMemoryDriver()
		d.Start()
		return d, nil
	})

	// The redis driver is NOT registered here. It lives in the queue/redis
	// leaf package (which carries the go-redis dependency) and self-registers
	// from its own init(). Blank-import github.com/velocitykode/velocity/queue/redis
	// or github.com/velocitykode/velocity/queue/standard to enable it.

	Drivers().Register("database", func(_ context.Context, cfg Config) (Driver, error) {
		if cfg.DB == nil {
			return nil, fmt.Errorf("velocity/queue: database driver requires a *sql.DB in Config.DB")
		}
		if cfg.DBDriver == "" {
			return nil, fmt.Errorf("velocity/queue: database driver requires DBDriver (postgres, mysql, or sqlite)")
		}
		return NewDatabaseDriver(cfg.DB, cfg.DBDriver), nil
	})
}

// NewQueue creates a new queue driver from the given configuration. The
// driver name is resolved through the canonical driver registry, so
// third-party drivers registered via Drivers().Register are available
// alongside the built-in memory / redis / database backends.
//
// An empty Driver field defaults to "memory" so zero-value configs work
// for tests and quick-start scenarios without surfacing a not-found
// error.
func NewQueue(config Config) (Driver, error) {
	return NewQueueWithContext(context.Background(), config)
}

// NewQueueWithContext is the context-aware variant of NewQueue. The ctx
// is forwarded to the driver factory so drivers performing network I/O
// at construction can honour deadlines.
func NewQueueWithContext(ctx context.Context, config Config) (Driver, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	driver := config.Driver
	if driver == "" {
		driver = "memory"
	}
	d, err := drivers.Resolve(ctx, driver, config)
	if err != nil {
		return nil, errchain.Errorf("velocity/queue: %w", err)
	}
	if config.Logger != nil {
		if la, ok := d.(contract.LoggerAware); ok {
			la.SetLogger(config.Logger)
		}
	}
	return d, nil
}
