package queue

import (
	"fmt"
	"strings"
)

// QueueConfig holds configuration for creating a queue driver.
type QueueConfig struct {
	// Driver is the queue driver to use: "memory", "redis", or "database".
	Driver string

	// Redis holds Redis-specific configuration. Required when Driver is "redis".
	Redis RedisConfig
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
		return NewDatabaseDriver(), nil
	default:
		return nil, fmt.Errorf("unknown queue driver: %s", driver)
	}
}

// init initializes the queue package.
// Use NewQueue() to create queue instances explicitly.
func init() {
	// No-op: global singleton is no longer eagerly initialized.
	// Queue instances should be created explicitly via NewQueue().
}

// NewRedisQueue creates a Redis queue from environment config.
func NewRedisQueue() (Driver, error) {
	return nil, fmt.Errorf("NewRedisQueue is deprecated: use NewQueue(QueueConfig{Driver: \"redis\", Redis: RedisConfig{...}}) instead")
}

// NewDatabaseQueue creates a database queue from environment config.
func NewDatabaseQueue() (Driver, error) {
	return NewDatabaseDriver(), nil
}

// NewMemoryQueue creates an in-memory queue.
func NewMemoryQueue() Driver {
	return NewMemoryDriver()
}
