package queue

import (
	"database/sql"
	"fmt"
	"strings"
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

// NewQueue creates a new queue driver from the given configuration.
func NewQueue(config QueueConfig) (Driver, error) {
	driver := strings.ToLower(config.Driver)
	if driver == "" {
		driver = "memory"
	}

	switch driver {
	case "memory":
		return NewMemoryDriver(), nil
	case "redis":
		return NewRedisDriver(config.Redis)
	case "database":
		if config.DB == nil {
			return nil, fmt.Errorf("database queue driver requires a *sql.DB in QueueConfig.DB")
		}
		return NewDatabaseDriver(config.DB, config.DBDriver), nil
	default:
		return nil, fmt.Errorf("unknown queue driver: %s", driver)
	}
}
