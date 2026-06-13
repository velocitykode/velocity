package contract

import (
	"context"
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
	//
	// Concrete-type fidelity depends on the driver. The in-memory store keeps
	// live Go values and returns the exact concrete type that was Put. The
	// serializing stores (redis, file) round-trip values through JSON, so a
	// struct comes back as map[string]interface{} and a number as float64,
	// regardless of the type originally stored. Callers that need the original
	// concrete type back across all drivers should use cache.GetAs[T], which
	// re-decodes into T on the serializing path. (Strings, including binary /
	// invalid-UTF-8 strings, do round-trip byte-identically on every driver.)
	GetCtx(ctx context.Context, key string) (interface{}, bool)

	// Deprecated: use GetCtx with a request-scoped context.Context.
	Get(key string) (interface{}, bool)

	// GetStringCtx retrieves a string value from the cache.
	GetStringCtx(ctx context.Context, key string) (string, bool)

	// Deprecated: use GetStringCtx with a request-scoped context.Context.
	GetString(key string) (string, bool)

	// PutCtx stores a value in the cache with a TTL.
	//
	// TTL contract: ttl > 0 expires the entry after that duration; ttl <= 0
	// stores the value forever (no expiration), identical to ForeverCtx. A
	// ttl of 0 therefore never writes an already-expired entry. Every Store
	// implementation honours this; it is enforced by cachetest.
	PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error

	// Deprecated: use PutCtx with a request-scoped context.Context.
	Put(key string, value interface{}, ttl time.Duration) error

	// AddCtx atomically stores a value in the cache with a TTL only if the
	// key does not already exist. Returns true if the value was inserted,
	// false if the key already existed (no write performed). Returns an
	// error only on backend failure; contention (key already present) is
	// reported as (false, nil).
	//
	// The same TTL contract as PutCtx applies: ttl <= 0 inserts the entry
	// forever rather than already-expired.
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

	// IncrementCtx increments a numeric value, treating a missing key as
	// zero so the post-increment result equals the supplied delta on
	// first call. The new value is returned. Enforced by
	// cachetest.IncrementCtx_NewKey_StartsFromZero.
	IncrementCtx(ctx context.Context, key string, value int64) (int64, error)

	// Deprecated: use IncrementCtx with a request-scoped context.Context.
	Increment(key string, value int64) (int64, error)

	// DecrementCtx decrements a numeric value, treating a missing key as
	// zero (a Decrement on a fresh key returns -delta). Symmetric with
	// IncrementCtx.
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

	// PutManyCtx stores multiple values. The PutCtx TTL contract applies to
	// every entry in the batch: ttl <= 0 stores them all forever.
	PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error

	// Deprecated: use PutManyCtx with a request-scoped context.Context.
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// HasCtx checks if a key exists.
	HasCtx(ctx context.Context, key string) bool

	// Deprecated: use HasCtx with a request-scoped context.Context.
	Has(key string) bool
}

// CacheStore represents a cache store with a prefix.
//
// Implementations must pass cachetest.RunStoreContractTests. Stores that
// additionally implement drivers.Locker must pass
// cachetest.RunLockerContractTests. See cachetest for the executable
// specification.
type CacheStore interface {
	Cache
	GetPrefix() string
}

// CacheLock defines the interface for a cache lock.
//
// All methods that perform I/O or may block accept a context so callers can
// propagate cancellation and deadlines (e.g. a request-scoped ctx) into the
// underlying driver. Drivers that do not perform I/O (memory) honor ctx
// cancellation where it affects blocking behavior.
type CacheLock interface {
	// Get attempts to acquire the lock. Returns true if acquired.
	// Honors ctx cancellation: returns false if ctx is cancelled before
	// the underlying driver completes.
	//
	// Get collapses backend errors and contention into a single boolean
	// return. Callers that need to distinguish "another holder owns the
	// key" from "Redis connection dropped / AUTH failure / OOM" MUST
	// use GetWithErr instead. The scheduler's distributed-Locker
	// adapter uses GetWithErr so a Redis outage surfaces as a
	// WARN-level "skip with backend error" event instead of a silent
	// "skip as if contended".
	Get(ctx context.Context) bool

	// GetWithErr is the error-returning variant of Get. The bool is
	// "was the lock acquired?"; the error is non-nil iff the backend
	// itself failed (e.g. Redis network reset, AUTH failure, OOM).
	//
	// Return-value contract: (true, nil) means the lock was acquired
	// by this caller. (false, nil) means contention, another holder
	// owns the key. (any, err != nil) means backend failure -- the
	// bool MUST be ignored and callers MUST NOT treat the call as
	// "lock is held by another caller", because the lock state is
	// undefined.
	//
	// Drivers that do not perform I/O (memory) always return a nil
	// error. Drivers that perform I/O (redis, database) return the
	// underlying client error verbatim so callers can inspect it.
	GetWithErr(ctx context.Context) (bool, error)

	// Release releases the lock only if the current instance is the owner.
	// Returns true if the lock was successfully released.
	Release(ctx context.Context) bool

	// Run acquires the lock, invokes callback, and releases the lock.
	// Returns ErrLockNotAcquired if the lock cannot be acquired.
	// If callback panics, the lock is still released and the panic propagates.
	Run(ctx context.Context, callback func()) error

	// Block polls for the lock up to timeout (retrying every 100ms) then
	// invokes callback under the lock. Returns ErrLockTimeout on timeout,
	// or ctx.Err() if ctx is cancelled before the lock is acquired.
	Block(ctx context.Context, timeout time.Duration, callback func()) error

	// Owner returns the owner identifier of this lock instance.
	Owner() string

	// ForceRelease deletes the lock key without checking the owner.
	ForceRelease(ctx context.Context) error
}

// CacheManager is the interface satisfied by *cache.Manager. It covers the
// methods used through app.Services and router.Context for cache operations,
// store management, locking, and event wiring.
type CacheManager interface {
	// Basic operations on the default store.
	Get(key string) (interface{}, bool)
	GetWithContext(ctx context.Context, key string) (interface{}, bool)
	GetString(key string) (string, bool)
	Put(key string, value interface{}, ttl time.Duration) error
	PutWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Add(key string, value interface{}, ttl time.Duration) (bool, error)
	AddWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)
	Forever(key string, value interface{}) error
	ForeverWithContext(ctx context.Context, key string, value interface{}) error
	Forget(key string) error
	ForgetWithContext(ctx context.Context, key string) error
	Flush() error
	Has(key string) bool
	Increment(key string, value int64) (int64, error)
	Decrement(key string, value int64) (int64, error)
	Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error)
	RememberWithContext(ctx context.Context, key string, ttl time.Duration, callback func() interface{}) (interface{}, error)
	RememberE(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
	RememberEWithContext(ctx context.Context, key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
	RememberForever(key string, callback func() interface{}) (interface{}, error)
	RememberForeverWithContext(ctx context.Context, key string, callback func() interface{}) (interface{}, error)
	RememberForeverE(key string, callback func() (interface{}, error)) (interface{}, error)
	RememberForeverEWithContext(ctx context.Context, key string, callback func() (interface{}, error)) (interface{}, error)
	Many(keys []string) map[string]interface{}
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// Store management.
	Store(name string) (CacheStore, error)
	StoreWithContext(ctx context.Context, name string) (CacheStore, error)
	DefaultStore() (CacheStore, error)
	DefaultStoreWithContext(ctx context.Context) (CacheStore, error)
	Shutdown(ctx context.Context) error

	// Distributed locking.
	Lock(key string, ttl ...time.Duration) CacheLock
	RestoreLock(key string, owner string) CacheLock

	// Event wiring.
	SetEventDispatcher(fn func(ctx context.Context, event interface{}) error)
}
