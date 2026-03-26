package drivers

import (
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
type Lock interface {
	Get() bool
	Release() bool
	Run(callback func()) error
	Block(timeout time.Duration, callback func()) error
	Owner() string
	ForceRelease() error
}

// Locker is implemented by stores that support locking.
type Locker interface {
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock
}
