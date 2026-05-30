package queue

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/velocitykode/velocity/driverregistry"
)

// QueueConfig holds configuration for creating a queue driver.
type QueueConfig struct {
	// Driver is the queue driver to use: "memory", "redis", or "database".
	Driver string

	// Redis holds Redis-specific configuration. Required when Driver is "redis".
	Redis RedisConfig

	// DB holds a *sql.DB for the database driver. Required when Driver is "database".
	DB *sql.DB

	// DBDriver specifies the database driver name ("postgres", "mysql", "sqlite").
	// Required when Driver is "database".
	DBDriver string
}

// Validate checks the QueueConfig for structural problems before NewQueue
// resolves a driver factory. The database driver requires DB and DBDriver
// to be set; the redis driver does not validate Redis credentials here so
// dev fixtures can boot with empty defaults that the local redis allows.
// Per-driver factory errors still surface from Drivers().Resolve.
func (c QueueConfig) Validate() error {
	if c.Driver == "" {
		return nil
	}
	if c.Driver == "database" {
		if c.DB == nil {
			return fmt.Errorf("velocity/queue: QUEUE_DRIVER=database requires a non-nil *sql.DB")
		}
		if c.DBDriver == "" {
			return fmt.Errorf("velocity/queue: QUEUE_DRIVER=database requires DBDriver (postgres, mysql, or sqlite)")
		}
	}
	return nil
}

// drivers is the canonical Velocity driver registry for queues. Built-in
// drivers (memory, redis, database) self-register from this file's init();
// third-party queue backends can register additional factories.
var drivers = driverregistry.New[Driver, QueueConfig]("queue")

// Drivers returns the registry that queue drivers register themselves
// into. Use this from a driver package's init() to install a factory:
//
//	func init() {
//	    queue.Drivers().Register("kafka", func(ctx context.Context, cfg queue.QueueConfig) (queue.Driver, error) {
//	        return newKafkaDriver(cfg), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Driver, QueueConfig] { return drivers }

func init() {
	Drivers().Register("memory", func(_ context.Context, _ QueueConfig) (Driver, error) {
		d := NewMemoryDriver()
		d.Start()
		return d, nil
	})

	// The redis driver is NOT registered here. It lives in the queue/redis
	// leaf package (which carries the go-redis dependency) and self-registers
	// from its own init(). Blank-import github.com/velocitykode/velocity/queue/redis
	// or github.com/velocitykode/velocity/queue/standard to enable it.

	Drivers().Register("database", func(_ context.Context, cfg QueueConfig) (Driver, error) {
		if cfg.DB == nil {
			return nil, fmt.Errorf("velocity/queue: database driver requires a *sql.DB in QueueConfig.DB")
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
func NewQueue(config QueueConfig) (Driver, error) {
	return NewQueueWithContext(context.Background(), config)
}

// NewQueueWithContext is the context-aware variant of NewQueue. The ctx
// is forwarded to the driver factory so drivers performing network I/O
// at construction can honour deadlines.
func NewQueueWithContext(ctx context.Context, config QueueConfig) (Driver, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	driver := config.Driver
	if driver == "" {
		driver = "memory"
	}
	d, err := drivers.Resolve(ctx, driver, config)
	if err != nil {
		return nil, fmt.Errorf("velocity/queue: %w", err)
	}
	return d, nil
}
