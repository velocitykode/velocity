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

// installSchedulerLocker is called by velocity.New after both cache and
// scheduler are constructed. When the configured cache driver is
// anything other than "memory" (i.e. file, redis, database -- any shared
// or persistent backend), the scheduler's process-local InMemoryLocker
// default is replaced with a cache-backed adapter. For "memory" cache
// deployments (typically dev/test single-host) the InMemoryLocker is
// left in place: a per-process cache and a per-process Locker have the
// same scope, and swapping in the adapter would not improve correctness
// while making the test surface noisier.
//
// Pass-through helper centralises the "is this driver shareable?"
// decision; future drivers (memcached, dynamodb, etc.) only need to be
// added to the deny-list once.
func installSchedulerLocker(sched *scheduler.Scheduler, cm cache.CacheManager, driver string) {
	if sched == nil || cm == nil {
		return
	}
	if driver == "" || driver == "memory" {
		return
	}
	if l := newCacheLocker(cm); l != nil {
		sched.SetLocker(l)
	}
}
