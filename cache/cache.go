package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// GetAs retrieves a value from the store and returns it typed as T. It bridges
// the type-fidelity gap between drivers: the in-memory store keeps live Go
// values and returns the concrete type directly, while the serializing stores
// (redis, file) round-trip values through JSON and therefore hand back decoded
// shapes -- map[string]interface{} for a struct, float64 for an integer. GetAs
// returns T regardless of driver:
//
//   - if the stored value is already a T (memory, or a builtin match), it is
//     returned as-is, with no re-encoding;
//   - otherwise the JSON-decoded value is re-encoded and decoded into T, so a
//     struct stored on redis/file comes back as the concrete struct and an
//     integer comes back as the requested integer type.
//
// Returns (zero, false) on a miss or when the stored value cannot be converted
// to T.
//
// Precision caveat: on the serializing drivers (redis, file) GetCtx decodes JSON
// numbers into float64 before GetAs runs, so an integer larger than 2^53 stored
// on those drivers may already have lost precision by the time GetAs re-decodes
// it -- GetAs cannot recover bits that JSON decoding dropped. The in-memory
// store keeps the exact value, so GetAs[int64] there is exact. If you need exact
// large integers on a serializing driver, store them as a string and parse on
// read.
//
// Go 1.26 method generics have not shipped, so this is a package-level function
// rather than a Store method (same shape as RememberT).
func GetAs[T any](store Store, key string) (T, bool) {
	return GetAsWithContext[T](context.Background(), store, key)
}

// GetAsWithContext is the ctx-aware counterpart of GetAs, threading ctx through
// to the store's GetCtx.
func GetAsWithContext[T any](ctx context.Context, store Store, key string) (T, bool) {
	var zero T
	v, ok := store.GetCtx(ctx, key)
	if !ok {
		return zero, false
	}
	// Fast path: already the concrete type (memory driver, or a builtin that
	// survived JSON as T such as string/bool).
	if typed, ok := v.(T); ok {
		return typed, true
	}
	// Slow path: re-encode the JSON-decoded shape into the concrete T.
	data, err := json.Marshal(v)
	if err != nil {
		return zero, false
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return zero, false
	}
	return out, true
}

// Cache defines the interface for cache operations. Canonical declaration
// lives in the stdlib-only contract leaf; this alias keeps the cache API
// byte-identical for existing callers.
type Cache = contract.Cache

// Store represents a cache store with a prefix. Canonical declaration lives
// in the contract leaf as CacheStore.
//
// TTL contract: across every Store method that takes a ttl (PutCtx, AddCtx,
// PutManyCtx), ttl > 0 sets an expiration that far ahead and ttl <= 0 stores
// the value forever -- never an already-expired entry. ForeverCtx is the
// explicit forever form. Enforced by cachetest.
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
