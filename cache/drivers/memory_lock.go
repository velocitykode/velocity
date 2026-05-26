package drivers

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// memoryLockEntry represents a single lock held in memory.
type memoryLockEntry struct {
	owner     string
	expiresAt *time.Time
}

// memoryLockStore tracks which keys are locked and by whom.
type memoryLockStore struct {
	mu    sync.Mutex
	locks map[string]*memoryLockEntry
}

func newMemoryLockStore() *memoryLockStore {
	return &memoryLockStore{
		locks: make(map[string]*memoryLockEntry),
	}
}

// acquire attempts to set the lock for the given key/owner if it is unlocked or expired.
func (s *memoryLockStore) acquire(key, owner string, ttl time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entry, exists := s.locks[key]; exists {
		if entry.expiresAt != nil && time.Now().After(*entry.expiresAt) {
			delete(s.locks, key)
		} else {
			return false
		}
	}

	entry := &memoryLockEntry{owner: owner}
	if ttl > 0 {
		exp := time.Now().Add(ttl)
		entry.expiresAt = &exp
	}
	s.locks[key] = entry
	return true
}

// release removes the lock only if the caller is the owner.
func (s *memoryLockStore) release(key, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, exists := s.locks[key]
	if !exists {
		return false
	}
	if entry.owner != owner {
		return false
	}
	delete(s.locks, key)
	return true
}

// forceRelease removes the lock regardless of owner.
func (s *memoryLockStore) forceRelease(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.locks, key)
	return nil
}

// MemoryLock implements an in-memory lock for development and testing.
type MemoryLock struct {
	store *memoryLockStore
	key   string
	owner string
	ttl   time.Duration
}

// NewMemoryLock creates a new MemoryLock bound to the given lock store.
func NewMemoryLock(store *memoryLockStore, key string, owner string, ttl time.Duration) *MemoryLock {
	return &MemoryLock{
		store: store,
		key:   key,
		owner: owner,
		ttl:   ttl,
	}
}

// Get attempts to acquire the lock. Returns true if the lock was acquired.
// ctx is accepted for interface uniformity with RedisLock; because acquisition
// is an in-process map update it never blocks, but a pre-cancelled ctx is
// respected (returns false). Backend-error vs contention distinction is
// not possible here (the operation is a synchronous map update with no
// failure mode beyond ctx cancellation), but the GetWithErr surface is
// implemented so callers compile against the full Lock interface.
func (l *MemoryLock) Get(ctx context.Context) bool {
	acquired, _ := l.GetWithErr(ctx)
	return acquired
}

// GetWithErr is the error-returning variant. For an in-memory lock the
// error is non-nil iff the lock was constructed with a non-positive TTL
// (ErrInvalidLockTTL); ctx cancellation surfaces as (false, nil)
// because there is no underlying I/O that could be cancelled mid-flight
// -- the acquire is a synchronous map update.
//
// A zero/negative TTL means "never expires", which is dangerous: a
// holder that crashes between Get and Release pins the key forever and
// every subsequent acquirer blocks indefinitely. Rather than silently
// promoting that to a permanent hold we surface ErrInvalidLockTTL so
// the caller realises they forgot to pass a TTL.
func (l *MemoryLock) GetWithErr(ctx context.Context) (bool, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false, nil
		}
	}
	if l.ttl <= 0 {
		return false, ErrInvalidLockTTL
	}
	return l.store.acquire(l.key, l.owner, l.ttl), nil
}

// Release releases the lock only if the current instance is the owner.
// Returns true if the lock was successfully released.
func (l *MemoryLock) Release(ctx context.Context) bool {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return false
		}
	}
	return l.store.release(l.key, l.owner)
}

// Run acquires the lock, runs the callback, and releases the lock.
// Returns ErrLockNotAcquired if the lock cannot be acquired.
// If the callback panics, the lock is still released and the panic propagates.
func (l *MemoryLock) Run(ctx context.Context, callback func()) error {
	if !l.Get(ctx) {
		return ErrLockNotAcquired
	}
	defer l.Release(ctx)

	callback()
	return nil
}

// Block attempts to acquire the lock within the given timeout, retrying every 100ms.
// Once acquired, it runs the callback and releases the lock.
// Returns ErrLockTimeout if the lock cannot be acquired within the timeout,
// or ctx.Err() if ctx is cancelled before acquisition.
// If the callback panics, the lock is still released and the panic propagates.
func (l *MemoryLock) Block(ctx context.Context, timeout time.Duration, callback func()) error {
	deadline := time.Now().Add(timeout)

	for {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
		}

		if l.Get(ctx) {
			defer l.Release(ctx)
			callback()
			return nil
		}

		if time.Now().After(deadline) {
			return ErrLockTimeout
		}

		// Sleep but wake early on ctx cancellation so Block returns promptly.
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// Owner returns the owner identifier of this lock.
func (l *MemoryLock) Owner() string {
	return l.owner
}

// ForceRelease deletes the lock key without checking the owner.
func (l *MemoryLock) ForceRelease(ctx context.Context) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return l.store.forceRelease(l.key)
}

// Lock creates a new MemoryLock for the given key with an optional TTL.
// The lock store is scoped to this MemoryStore instance.
func (s *MemoryStore) Lock(key string, ttl ...time.Duration) Lock {
	lockTTL := time.Duration(0)
	if len(ttl) > 0 {
		lockTTL = ttl[0]
	}
	owner := uuid.New().String()
	return NewMemoryLock(s.lockStore, PrefixKey(s.prefix, "lock:"+key), owner, lockTTL)
}

// RestoreLock restores a lock instance for the given key and owner without acquiring it.
func (s *MemoryStore) RestoreLock(key string, owner string) Lock {
	return NewMemoryLock(s.lockStore, PrefixKey(s.prefix, "lock:"+key), owner, 0)
}
