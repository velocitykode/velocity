package cache

import (
	"context"
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

// ContextStore is an optional extension of Store that threads the caller's
// context.Context through to the underlying driver. Stores that implement
// this interface allow the cache manager to cancel long-running operations
// (e.g. Redis GETs across a slow network) when the request context is cancelled.
//
// The Manager uses contract assertions to call these methods when available
// and falls back to the plain Store methods otherwise, so drivers may adopt
// ContextStore incrementally.
type ContextStore interface {
	Store

	GetCtx(ctx context.Context, key string) (interface{}, bool)
	GetStringCtx(ctx context.Context, key string) (string, bool)
	PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	ForeverCtx(ctx context.Context, key string, value interface{}) error
	ForgetCtx(ctx context.Context, key string) error
	FlushCtx(ctx context.Context) error
	HasCtx(ctx context.Context, key string) bool
	IncrementCtx(ctx context.Context, key string, value int64) (int64, error)
	DecrementCtx(ctx context.Context, key string, value int64) (int64, error)
	ManyCtx(ctx context.Context, keys []string) map[string]interface{}
	PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error
}

// Driver types
const (
	DriverMemory   = "memory"
	DriverFile     = "file"
	DriverRedis    = "redis"
	DriverDatabase = "database"
)
