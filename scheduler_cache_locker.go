package velocity

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/scheduler"
)

// cacheLocker adapts cache.CacheManager into scheduler.Locker so the
// scheduler's WithoutOverlapping() and OnOneServer() lock contests run
// against a shared cache backend (Redis, file, database, ...) rather than
// the process-local scheduler.InMemoryLocker. Without this adapter, two
// Velocity hosts each running scheduler.Run would both fire an
// OnOneServer() job on every tick: C-04's documented worst case.
//
// The adapter delegates the real "set if not exists with TTL" semantics
// to the cache.Lock backend (memoryLockStore, redis SET NX EX, etc.) and
// issues a strictly increasing fencing token from a package-level
// atomic counter so the scheduler.Lock contract (monotonic tokens
// per-name across successful acquisitions) is honored. Token monotonicity
// across process restarts is NOT preserved -- that would require
// persistent counter state and is out of scope here; the in-memory
// counter still gives the required ordering within a process, which is
// what Lock holders use to fence each other.
type cacheLocker struct {
	cm cache.CacheManager
}

// fencingTokenCounter is the package-level monotonic source used by
// cacheLocker. atomic.Uint64 wraps after ~584 years at 1 acquire/ns so
// overflow is not a practical concern.
var fencingTokenCounter atomic.Uint64

// newCacheLocker constructs a scheduler.Locker that delegates to the
// given cache manager. Returns nil if cm is nil so callers can chain
// without a separate nil-check; scheduler.SetLocker(nil) installs an
// InMemoryLocker fallback.
func newCacheLocker(cm cache.CacheManager) scheduler.Locker {
	if cm == nil {
		return nil
	}
	return &cacheLocker{cm: cm}
}

// Acquire implements scheduler.Locker. Wraps cache.Manager.Lock(name, ttl)
// + cache.Lock.Get(ctx). Returns scheduler.ErrLockHeld (wrapped) when the
// backend reports the key is already held; any other failure (nil lock
// from a store that does not support locking) is surfaced as a typed
// error so callers can distinguish "contention" from "misconfiguration".
func (l *cacheLocker) Acquire(ctx context.Context, name string, ttl time.Duration) (scheduler.Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lk := l.cm.Lock(name, ttl)
	if lk == nil {
		// The default cache store does not support locking. Surface a
		// distinct error so the scheduler's debug log can name the
		// misconfiguration; this is NOT scheduler.ErrLockHeld (which
		// means "another holder has it"), it means "no backend at all".
		return nil, fmt.Errorf("velocity: cache store does not implement Locker for key %q", name)
	}
	if !lk.Get(ctx) {
		// Already held by another caller (or this caller's previous
		// holder whose TTL has not yet expired). Match the scheduler's
		// contention contract so runDueJobs can skip silently.
		return nil, fmt.Errorf("velocity: cache lock %q: %w", name, scheduler.ErrLockHeld)
	}
	return &cacheLockHandle{
		name:  name,
		token: fencingTokenCounter.Add(1),
		inner: lk,
	}, nil
}

// cacheLockHandle is a scheduler.Lock that wraps a cache.Lock. Release()
// is idempotent: the cache.Lock.Release contract returns false when the
// caller is not the owner (or the lock has already expired), and we
// translate that into a nil error so a deferred release path does not
// observe spurious failures after TTL expiry.
type cacheLockHandle struct {
	name     string
	token    uint64
	inner    cache.Lock
	released atomic.Bool
}

// Name returns the lock name (the cache key).
func (h *cacheLockHandle) Name() string { return h.name }

// FencingToken returns the monotonically increasing token issued at
// Acquire time. Strictly increasing per-process; see fencingTokenCounter
// docstring on cross-process semantics.
func (h *cacheLockHandle) FencingToken() uint64 { return h.token }

// Release implements scheduler.Lock. Idempotent: a second call is a
// no-op. Translates the cache.Lock's bool return into an error contract
// (nil on success, nil on already-released / TTL-expired -- nothing the
// caller can act on; the lock is gone either way).
func (h *cacheLockHandle) Release(ctx context.Context) error {
	if h.released.Swap(true) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// The cache.Lock.Release return value is informational only --
	// "false" usually means "the lock has already expired by TTL or was
	// force-released by an admin". The scheduler treats either case as
	// successful release (the holder no longer has the lock); surfacing
	// an error here would just be noise.
	_ = h.inner.Release(ctx)
	return nil
}

// lockCapable is the structural interface a cache store must satisfy
// for cacheLocker to be installable. It matches cache/drivers.Locker
// without forcing scheduler_cache_locker.go to import the drivers
// package directly: a *MemoryStore and *RedisStore satisfy it, a
// *FileStore and a *DatabaseStore (the latter not yet implemented at
// all) do NOT, because their Lock method is absent.
//
// The match is structural -- if a future cache driver implements the
// same two-method shape, capability is detected automatically with no
// changes to this file.
type lockCapable interface {
	Lock(key string, ttl ...time.Duration) cache.Lock
	RestoreLock(key string, owner string) cache.Lock
}

// installerLogger is the narrow log surface installSchedulerLocker
// uses to emit the fallback WARN. Implemented by *log.Logger. Defining
// it locally keeps this file's import set unchanged and lets tests
// inject a capture logger without depending on the log package.
type installerLogger interface {
	Warn(msg string, kvs ...any)
}

// installSchedulerLocker is called by velocity.New after both cache and
// scheduler are constructed. It decides which Locker the scheduler will
// use for WithoutOverlapping() and OnOneServer() contests:
//
//   - driver == "" or "memory": leave the scheduler.InMemoryLocker
//     default in place. A per-process cache and a per-process Locker
//     have the same scope; swapping in the adapter would not improve
//     correctness.
//
//   - any other driver, BUT the underlying store does not implement
//     the Lock primitive (file, database -- see lockCapable docstring):
//     fall back to scheduler.InMemoryLocker and emit a WARN log. This
//     preserves single-process correctness; multi-host operators get
//     a loud signal that distributed locking is NOT active. Without
//     this fallback, cacheLocker.Acquire would surface the
//     misconfiguration error, the scheduler would treat it as
//     contention, and every WithoutOverlapping / OnOneServer job
//     would be silently skipped forever -- the C-04 worst case
//     re-introduced via a different path.
//
//   - any other driver AND the store implements Lock (redis today):
//     install the cache-backed adapter; cluster-wide guarantees hold.
//
// Pass-through helper centralises the capability decision; future
// drivers that grow Lock support only need to satisfy the lockCapable
// interface and no changes here are required.
func installSchedulerLocker(sched *scheduler.Scheduler, cm cache.CacheManager, driver string, log installerLogger) {
	if sched == nil || cm == nil {
		return
	}
	if driver == "" || driver == "memory" {
		return
	}

	// Probe the default store for Lock capability. If the cache has
	// not been initialised (e.g. cm is a mock that returns an error),
	// or the store does not implement the lock primitive, the
	// distributed-Locker path is unsafe and we fall back to in-process
	// semantics. The driver name is logged so the warning is
	// actionable.
	store, err := cm.DefaultStore()
	if err != nil || store == nil {
		if log != nil {
			log.Warn(
				"velocity/scheduler: cache default store unavailable; falling back to in-process Locker; multi-host OnOneServer / WithoutOverlapping will NOT work",
				"driver", driver,
				"error", err,
			)
		}
		return
	}
	if _, ok := store.(lockCapable); !ok {
		if log != nil {
			log.Warn(
				"velocity/scheduler: cache driver does not support distributed locks; falling back to in-process Locker; multi-host OnOneServer / WithoutOverlapping will NOT work",
				"driver", driver,
			)
		}
		return
	}

	if l := newCacheLocker(cm); l != nil {
		sched.SetLocker(l)
	}
}
