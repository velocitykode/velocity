package drivers

import (
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
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

// Lock defines the interface for a cache lock. Canonical declaration lives in
// the stdlib-only contract leaf as CacheLock; this alias keeps the drivers
// API byte-identical for existing callers.
type Lock = contract.CacheLock

// Conformance assertions: built-in drivers satisfy the contract interfaces.
// The FileLock assertion lives in file_lock.go under the unix build tag,
// alongside the type it asserts.
var (
	_ contract.CacheStore = (*MemoryStore)(nil)
	_ contract.CacheStore = (*FileStore)(nil)
	_ contract.CacheLock  = (*MemoryLock)(nil)
)

// Locker is implemented by stores that support locking.
//
// Lock takes a positive TTL. Callers MUST pass a non-zero positive
// duration: a zero or negative TTL is rejected at acquisition with
// ErrInvalidLockTTL because an unbounded hold pins the key forever
// when the holding process crashes. The variadic shape is preserved
// for source-compat with earlier callers, but supplying zero ttls is
// now an error.
//
// Implementations must pass cachetest.RunLockerContractTests. See
// cachetest for the executable specification.
type Locker interface {
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock
}
