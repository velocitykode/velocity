package drivers

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrLockNotAcquired is returned when a lock cannot be acquired.
	ErrLockNotAcquired = errors.New("cache: lock not acquired")

	// ErrLockTimeout is returned when a lock wait exceeds the given timeout.
	ErrLockTimeout = errors.New("cache: lock wait timeout exceeded")

	// ErrLockNotSupported is returned when the underlying store does
	// not provide a distributed-lock implementation for the running
	// platform (e.g. file driver on Windows where flock(2) is
	// unavailable). Callers should switch to a different driver or
	// avoid Lock on that store.
	ErrLockNotSupported = errors.New("cache: lock not supported on this driver/platform")

	// ErrInvalidLockTTL is returned when a caller asks for a lock with
	// a non-positive TTL. A zero/negative TTL means "never expire",
	// which is unsafe in a distributed setting: if the holder process
	// crashes between Get and Release the lock pins the key forever
	// and every subsequent acquirer blocks. Callers MUST pass a
	// positive ttl to Store.Lock so an abandoned hold eventually
	// frees itself.
	ErrInvalidLockTTL = errors.New("cache: lock requires a positive TTL")
)

// Lock defines the interface for a cache lock.
//
// All methods that perform I/O or may block accept a context so callers can
// propagate cancellation and deadlines (e.g. a request-scoped ctx) into the
// underlying driver. Drivers that do not perform I/O (memory) honor ctx
// cancellation where it affects blocking behavior.
type Lock interface {
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

// Locker is implemented by stores that support locking.
//
// Lock takes a positive TTL. Callers MUST pass a non-zero positive
// duration: a zero or negative TTL is rejected at acquisition with
// ErrInvalidLockTTL because an unbounded hold pins the key forever
// when the holding process crashes. The variadic shape is preserved
// for source-compat with earlier callers, but supplying zero ttls is
// now an error.
type Locker interface {
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock
}
