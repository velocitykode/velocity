package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Lock is a named mutual-exclusion primitive that a scheduler job can acquire
// so only one runner at a time executes a task across a distributed fleet.
// Implementations MUST issue a monotonically increasing fencing token for
// every successful Acquire call — the token lets downstream systems reject
// writes from an expired holder and is the canonical defence against the
// "stale lock" problem (see https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html).
type Lock interface {
	// Name returns the lock name (e.g. the job name it guards).
	Name() string

	// FencingToken returns the token issued at Acquire time. The value is
	// strictly increasing across successful acquisitions of the same name.
	FencingToken() uint64

	// Release drops the lock. Implementations MUST be idempotent — releasing
	// an already-released lock is not an error.
	Release(ctx context.Context) error
}

// Locker acquires named locks. It is intentionally narrow so backends
// (in-memory, Redis SET NX, ZooKeeper, etcd, database advisory locks) can be
// swapped without changing call sites.
type Locker interface {
	// Acquire takes the lock. The ttl bounds how long the holder may hold it
	// before implementations may reclaim it. Returns a non-nil Lock on
	// success or an error wrapping ErrLockHeld when already held.
	Acquire(ctx context.Context, name string, ttl time.Duration) (Lock, error)
}

// ErrLockHeld is returned by Locker.Acquire when the lock is currently held.
var ErrLockHeld = fmt.Errorf("velocity/scheduler: lock already held")

// InMemoryLocker is a process-local Locker suitable for single-instance
// deployments and tests. It is NOT safe to use across multiple processes —
// plug in a real distributed backend (Redis/ZooKeeper/etcd) for production.
type InMemoryLocker struct {
	mu    sync.Mutex
	token atomic.Uint64 // monotonically increasing fencing token
	locks map[string]*inMemoryLock
	nowFn func() time.Time // overridable for tests
}

// NewInMemoryLocker builds an in-memory Locker.
func NewInMemoryLocker() *InMemoryLocker {
	return &InMemoryLocker{
		locks: make(map[string]*inMemoryLock),
		nowFn: time.Now,
	}
}

type inMemoryLock struct {
	name     string
	token    uint64
	expires  time.Time
	locker   *InMemoryLocker
	released atomic.Bool
}

func (l *inMemoryLock) Name() string         { return l.name }
func (l *inMemoryLock) FencingToken() uint64 { return l.token }

func (l *inMemoryLock) Release(ctx context.Context) error {
	if l.released.Swap(true) {
		return nil // idempotent
	}
	l.locker.mu.Lock()
	defer l.locker.mu.Unlock()
	// Only clear the map entry if this exact token still owns the slot —
	// prevents a later holder (after TTL expiry) being released by an old
	// one.
	if existing, ok := l.locker.locks[l.name]; ok && existing.token == l.token {
		delete(l.locker.locks, l.name)
	}
	return nil
}

// Acquire takes the named lock or returns ErrLockHeld. A previously-held lock
// is considered free once its TTL expires; this matches typical Redis/ZK
// timeout-based semantics.
func (l *InMemoryLocker) Acquire(ctx context.Context, name string, ttl time.Duration) (Lock, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.nowFn()
	if existing, ok := l.locks[name]; ok && now.Before(existing.expires) {
		return nil, fmt.Errorf("velocity/scheduler: lock %q: %w", name, ErrLockHeld)
	}

	tok := l.token.Add(1)
	lk := &inMemoryLock{
		name:    name,
		token:   tok,
		expires: now.Add(ttl),
		locker:  l,
	}
	l.locks[name] = lk
	return lk, nil
}
