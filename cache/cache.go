package cache

import (
	"context"
	"fmt"
	"time"
)

// Cache defines the interface for cache operations.
//
// Methods come in pairs: a `Ctx`-suffixed variant that threads the caller's
// context.Context through to the underlying driver (so a slow Redis GET or
// disk fsync can be cancelled when the request context is cancelled), and a
// non-Ctx Deprecated shim that calls the Ctx variant with context.Background().
// New code MUST call the Ctx variant; non-Ctx methods remain for
// backward compatibility through the next release.
type Cache interface {
	// GetCtx retrieves a value from the cache.
	GetCtx(ctx context.Context, key string) (interface{}, bool)

	// Deprecated: use GetCtx with a request-scoped context.Context.
	Get(key string) (interface{}, bool)

	// GetStringCtx retrieves a string value from the cache.
	GetStringCtx(ctx context.Context, key string) (string, bool)

	// Deprecated: use GetStringCtx with a request-scoped context.Context.
	GetString(key string) (string, bool)

	// PutCtx stores a value in the cache with a TTL.
	PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error

	// Deprecated: use PutCtx with a request-scoped context.Context.
	Put(key string, value interface{}, ttl time.Duration) error

	// AddCtx atomically stores a value in the cache with a TTL only if the
	// key does not already exist. Returns true if the value was inserted,
	// false if the key already existed (no write performed). Returns an
	// error only on backend failure; contention (key already present) is
	// reported as (false, nil).
	//
	// Add is the SETNX primitive that lets callers gate single-flight
	// populates over the cache layer itself, avoiding the thundering-herd
	// problem on Remember-style cache-miss paths.
	AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)

	// Deprecated: use AddCtx with a request-scoped context.Context.
	Add(key string, value interface{}, ttl time.Duration) (bool, error)

	// ForeverCtx stores a value in the cache indefinitely.
	ForeverCtx(ctx context.Context, key string, value interface{}) error

	// Deprecated: use ForeverCtx with a request-scoped context.Context.
	Forever(key string, value interface{}) error

	// ForgetCtx removes a value from the cache.
	ForgetCtx(ctx context.Context, key string) error

	// Deprecated: use ForgetCtx with a request-scoped context.Context.
	Forget(key string) error

	// FlushCtx removes all values from the cache.
	FlushCtx(ctx context.Context) error

	// Deprecated: use FlushCtx with a request-scoped context.Context.
	Flush() error

	// IncrementCtx increments a numeric value.
	IncrementCtx(ctx context.Context, key string, value int64) (int64, error)

	// Deprecated: use IncrementCtx with a request-scoped context.Context.
	Increment(key string, value int64) (int64, error)

	// DecrementCtx decrements a numeric value.
	DecrementCtx(ctx context.Context, key string, value int64) (int64, error)

	// Deprecated: use DecrementCtx with a request-scoped context.Context.
	Decrement(key string, value int64) (int64, error)

	// Remember gets from cache or computes and stores. Pure-CPU callback;
	// no ctx threading point on the callback boundary itself, so no Ctx
	// variant on the Cache interface (Manager exposes RememberWithContext
	// at the consumer level).
	Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error)

	// RememberForever gets from cache or computes and stores forever.
	RememberForever(key string, callback func() interface{}) (interface{}, error)

	// ManyCtx retrieves multiple values.
	ManyCtx(ctx context.Context, keys []string) map[string]interface{}

	// Deprecated: use ManyCtx with a request-scoped context.Context.
	Many(keys []string) map[string]interface{}

	// PutManyCtx stores multiple values.
	PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error

	// Deprecated: use PutManyCtx with a request-scoped context.Context.
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// HasCtx checks if a key exists.
	HasCtx(ctx context.Context, key string) bool

	// Deprecated: use HasCtx with a request-scoped context.Context.
	Has(key string) bool
}

// Store represents a cache store with a prefix.
type Store interface {
	Cache
	GetPrefix() string
}

// ContextStore is a deprecated alias for Store. The ctx-aware methods that
// previously lived on this extension interface have been promoted into Store
// itself; all drivers now satisfy ContextStore by satisfying Store. Kept for
// one release so existing type assertions keep compiling.
//
// Deprecated: use Store directly; every Store now exposes the Ctx-suffixed
// methods.
type ContextStore = Store

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
