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
// to the cache.Lock backend (memoryLockStore, redis SET NX EX, etc.).
// Acquire uses cache.Lock.GetWithErr (not the bool-only Get) so a
// Redis outage surfaces as a distinct backend error rather than
// collapsing into "another host owns the lock" -- without that
// distinction, a Redis network reset would look identical to healthy
// contention and the scheduler would silently skip every guarded job
// until Redis recovered.
type cacheLocker struct {
	cm cache.CacheManager
}

// fencingTokenCounter is a process-local monotonic counter used by
// cacheLocker to populate Lock.FencingToken(). Tokens are STRICTLY
// process-local: each process starts at zero, so two processes
// acquiring the same key produce overlapping tokens. The scheduler
// does NOT use these tokens for cross-process write-side fencing
// today -- the field is informational. A future distributed fencing
// scheme that relies on cross-process monotonicity will need its own
// primitive (Redis INCR on a shared key, or a database SERIAL column,
// or an etcd revision), not this counter.
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
// + cache.Lock.GetWithErr(ctx). There are three distinct outcomes:
//
// (lock, nil): the cache backend returned (true, nil) from GetWithErr;
// we own the key for the configured TTL.
//
// (nil, wrapped scheduler.ErrLockHeld): backend returned (false, nil),
// i.e. another caller (or this caller's still-active previous holder)
// owns it. The scheduler treats this as quiet contention and skips
// the job at Debug log level.
//
// (nil, wrapped backend error): backend returned (any, err != nil).
// The lock state is undefined. The scheduler logs a Warn naming the
// underlying cause and skips the job; ops see the outage rather than
// a silent "appears contended forever" symptom.
//
// nil-lock-from-Lock-factory is treated as misconfiguration (the cache
// store does not implement Locker at all) and surfaced as a non-
// ErrLockHeld error so installSchedulerLocker's fallback path is the
// only place users should ever see this in practice.
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
	acquired, err := lk.GetWithErr(ctx)
	if err != nil {
		// Backend failure: connection dropped, AUTH/NOAUTH, OOM, slave
		// READONLY during a Redis failover, ctx cancellation, ... The
		// scheduler must NOT treat this as "another holder owns it"
		// (which would silently skip the job during the entire outage).
		// We wrap with a backend-error sentinel so the scheduler's
		// runDueJobs can errors.Is(err, ErrLockHeld) == false and log a
		// WARN naming the underlying cause.
		return nil, fmt.Errorf("velocity: cache lock %q backend error: %w", name, err)
	}
	if !acquired {
		// (false, nil) -- healthy contention. Another holder owns the
		// key (or this caller's previous holder still has it). Match
		// the scheduler's quiet-contention contract.
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

// FencingToken returns a process-local monotonic token. NOT safe for
// cross-process correctness -- two processes each acquiring the same
// key produce overlapping token sequences. See fencingTokenCounter
// docstring for why this is informational only. Real distributed
// fencing (rejecting writes from a stale lock holder) requires a
// different primitive and is out of scope here.
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
// package directly: *MemoryStore, *FileStore (on POSIX via flock(2)),
// and *RedisStore all satisfy it. The database driver does NOT
// because its factory returns "not yet implemented"; on Windows
// *FileStore also returns a nil Lock since flock(2) is unavailable.
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
//     the Lock primitive (database driver, or file driver on a
//     non-POSIX platform without flock -- see lockCapable docstring):
//     fall back to scheduler.InMemoryLocker and emit a WARN log. This
//     preserves single-process correctness; multi-host operators get
//     a loud signal that distributed locking is NOT active. Without
//     this fallback, cacheLocker.Acquire would surface the
//     misconfiguration error, the scheduler would treat it as
//     contention, and every WithoutOverlapping / OnOneServer job
//     would be silently skipped forever -- the C-04 worst case
//     re-introduced via a different path.
//
//   - any other driver AND the store implements Lock (redis; or file
//     on POSIX): install the cache-backed adapter. Redis gives true
//     cross-host guarantees; file gives single-host cross-process
//     guarantees via flock(2).
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
	lc, ok := store.(lockCapable)
	if !ok {
		if log != nil {
			log.Warn(
				"velocity/scheduler: cache driver does not support distributed locks; falling back to in-process Locker; multi-host OnOneServer / WithoutOverlapping will NOT work",
				"driver", driver,
			)
		}
		return
	}

	// Capability probe: *FileStore satisfies lockCapable structurally
	// on every platform because Lock is defined in both the unix and
	// non-unix build files, but the non-unix variant returns nil. A
	// nil result would otherwise look like a backend error on the
	// first scheduler tick. Probe with a throwaway key to confirm the
	// driver actually issues working locks; allow the lock to expire
	// by TTL rather than releasing explicitly (cheap, side-effect free
	// across runs).
	if probe := lc.Lock("__velocity_locker_probe__", time.Second); probe == nil {
		if log != nil {
			log.Warn(
				"velocity/scheduler: cache driver returned nil Lock during capability probe; falling back to in-process Locker; multi-host OnOneServer / WithoutOverlapping will NOT work",
				"driver", driver,
			)
		}
		return
	}

	if l := newCacheLocker(cm); l != nil {
		sched.SetLocker(l)
	}
}
