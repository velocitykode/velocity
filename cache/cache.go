package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Cache defines the interface for cache operations. Canonical declaration
// lives in the stdlib-only contract leaf; this alias keeps the cache API
// byte-identical for existing callers.
type Cache = contract.Cache

// Store represents a cache store with a prefix. Canonical declaration lives
// in the contract leaf as CacheStore.
//
// Implementations must pass cachetest.RunStoreContractTests. Stores that
// additionally implement drivers.Locker must pass
// cachetest.RunLockerContractTests. See cachetest for the executable
// specification.
type Store = contract.CacheStore

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
