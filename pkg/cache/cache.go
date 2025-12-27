package cache

import (
	"time"
)

// Cache defines the interface for cache operations
type Cache interface {
	// Get retrieves a value from the cache
	Get(key string) (interface{}, bool)

	// GetString retrieves a string value from the cache
	GetString(key string) (string, bool)

	// Put stores a value in the cache with a TTL
	Put(key string, value interface{}, ttl time.Duration) error

	// Forever stores a value in the cache indefinitely
	Forever(key string, value interface{}) error

	// Forget removes a value from the cache
	Forget(key string) error

	// Flush removes all values from the cache
	Flush() error

	// Increment increments a numeric value
	Increment(key string, value int64) (int64, error)

	// Decrement decrements a numeric value
	Decrement(key string, value int64) (int64, error)

	// Remember gets from cache or computes and stores
	Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error)

	// RememberForever gets from cache or computes and stores forever
	RememberForever(key string, callback func() interface{}) (interface{}, error)

	// Many retrieves multiple values
	Many(keys []string) map[string]interface{}

	// PutMany stores multiple values
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// Has checks if a key exists
	Has(key string) bool
}

// Store represents a cache store with a prefix
type Store interface {
	Cache
	GetPrefix() string
}

// Driver types
const (
	DriverMemory   = "memory"
	DriverFile     = "file"
	DriverRedis    = "redis"
	DriverDatabase = "database"
)