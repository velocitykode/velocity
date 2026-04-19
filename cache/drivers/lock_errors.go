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
	Get(ctx context.Context) bool

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
type Locker interface {
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock
}
