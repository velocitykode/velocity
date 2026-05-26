package cache

import (
	"context"
	"fmt"
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

	// Add atomically stores a value in the cache with a TTL only if the
	// key does not already exist. Returns true if the value was inserted,
	// false if the key already existed (no write performed). Returns an
	// error only on backend failure; contention (key already present) is
	// reported as (false, nil).
	//
	// Add is the SETNX primitive that lets callers gate single-flight
	// populates over the cache layer itself, avoiding the thundering-herd
	// problem on Remember-style cache-miss paths.
	Add(key string, value interface{}, ttl time.Duration) (bool, error)

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
	AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)
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

// RememberEable is the minimal surface RememberT needs from a cache target.
// *Manager satisfies it through its RememberE method.
type RememberEable interface {
	RememberE(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
}

// RememberEContextable is the ctx-aware counterpart of RememberEable.
// *Manager satisfies it through its RememberEWithContext method.
type RememberEContextable interface {
	RememberEWithContext(ctx context.Context, key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
}

// RememberT is a typed-generic shim over RememberE that returns T directly,
// avoiding the interface{} cast at every call site. Go 1.26 method generics
// have not shipped, so this is a package-level function rather than a
// Manager method.
//
// Usage:
//
//	region, err := cache.RememberT[Region](app.Cache, "regions", 5*time.Minute, func() (Region, error) {
//	    return upstream.FetchRegion()
//	})
//
// On callback error nothing is cached and the error is returned. On a type
// mismatch (cache contained a different type than T) the function returns the
// zero value of T and an error so callers can detect the corruption.
func RememberT[T any](mgr RememberEable, key string, ttl time.Duration, callback func() (T, error)) (T, error) {
	var zero T
	val, err := mgr.RememberE(key, ttl, func() (interface{}, error) {
		v, err := callback()
		if err != nil {
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	if val == nil {
		// Callback may legitimately return zero value; coerce to zero T.
		return zero, nil
	}
	typed, ok := val.(T)
	if !ok {
		return zero, fmt.Errorf("velocity/cache: cache slot %q holds %T, want %T", key, val, zero)
	}
	return typed, nil
}

// RememberTWithContext is the ctx-aware counterpart of RememberT, threading
// ctx through to the underlying store via RememberEWithContext.
//
// Usage:
//
//	region, err := cache.RememberTWithContext[Region](app.Cache, ctx, "regions", 5*time.Minute, func() (Region, error) {
//	    return upstream.FetchRegion(ctx)
//	})
func RememberTWithContext[T any](mgr RememberEContextable, ctx context.Context, key string, ttl time.Duration, callback func() (T, error)) (T, error) {
	var zero T
	val, err := mgr.RememberEWithContext(ctx, key, ttl, func() (interface{}, error) {
		v, err := callback()
		if err != nil {
			return nil, err
		}
		return v, nil
	})
	if err != nil {
		return zero, err
	}
	if val == nil {
		return zero, nil
	}
	typed, ok := val.(T)
	if !ok {
		return zero, fmt.Errorf("velocity/cache: cache slot %q holds %T, want %T", key, val, zero)
	}
	return typed, nil
}
