package drivers

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
)

// DefaultMaxEntries is the entry cap applied to a MemoryStore when no
// explicit cap is configured. The store is bounded by default so that
// attacker-influenceable cache keys (per-user, per-IP, per-request-derived)
// cannot grow the map without limit and OOM the process; the default is
// generous enough that well-behaved applications never hit it.
const DefaultMaxEntries = 1_000_000

// MemoryStore implements an in-memory cache store.
//
// The store holds at most maxEntries items. When an insert would exceed the
// cap, an approximately least-recently-used entry is evicted: every hit
// stamps the item with a monotonically increasing access sequence (a single
// atomic store, valid under the shared read lock), and eviction samples up
// to evictionSampleSize map entries and removes the stalest (preferring any
// already-expired entry it sees). Recency tracking therefore never mutates
// shared structure on reads, so cache hits stay on RLock and run
// concurrently; exact LRU would require moving a recency-list node on every
// hit under the exclusive lock, serializing all reads on the default
// (bounded) configuration. Stores holding no more entries than the sample
// size are evicted with exact LRU, since the sample covers the whole map.
type MemoryStore struct {
	mu        sync.RWMutex
	items     map[string]*cacheItem
	prefix    string
	ticker    *time.Ticker
	done      chan bool
	closeOnce sync.Once
	lockStore *memoryLockStore

	// accessSeq is the global recency clock; each hit stamps the item
	// with the next value. A uint64 cannot plausibly wrap.
	accessSeq atomic.Uint64

	// maxEntries is the resolved entry cap; 0 means unbounded.
	maxEntries int

	// maxValueBytes caps the serialized size of a single value accepted by
	// Put/Add/Forever; 0 means unlimited. Immutable after construction.
	maxValueBytes int64
}

// evictionSampleSize bounds how many map entries one eviction examines.
// Sampling trades exactness for keeping reads off the write lock; 16 keeps
// the stale-entry hit rate high (Redis defaults to 5) while keeping each
// eviction O(1)-ish.
const evictionSampleSize = 16

// cacheItem represents a cached item with expiration
type cacheItem struct {
	value      interface{}
	expiration *time.Time
	// lastAccess is the accessSeq stamp of the most recent hit or write.
	// Written atomically under RLock by reads; eviction reads it under
	// the exclusive lock.
	lastAccess atomic.Uint64
}

// MemoryOption configures a MemoryStore at construction time.
type MemoryOption func(*MemoryStore)

// WithMaxEntries sets the maximum number of entries the store may hold.
// n > 0 caps the store at n entries; n == 0 applies DefaultMaxEntries;
// n < 0 removes the bound entirely (documented escape hatch; an unbounded
// store is OOM-able when cache keys are attacker-influenceable).
func WithMaxEntries(n int) MemoryOption {
	return func(s *MemoryStore) {
		switch {
		case n > 0:
			s.maxEntries = n
		case n < 0:
			s.maxEntries = 0
		default:
			s.maxEntries = DefaultMaxEntries
		}
	}
}

// WithMaxValueBytes caps the serialized size of a single cached value.
// n > 0 rejects oversized Put/Add/Forever with ErrValueTooLarge; n <= 0
// (the default) means unlimited, preserving historical behaviour.
//
// Sizing requires serializing the value through the package serializer, so
// when a cap is set, values that cannot be serialized (channels, functions)
// are rejected with an error instead of being stored unmeasured. An
// uncapped store keeps accepting them unchanged.
func WithMaxValueBytes(n int64) MemoryOption {
	return func(s *MemoryStore) {
		if n > 0 {
			s.maxValueBytes = n
		} else {
			s.maxValueBytes = 0
		}
	}
}

// NewMemoryStore creates a new memory cache store, bounded at
// DefaultMaxEntries unless overridden via WithMaxEntries.
// Call Start() to begin the background expired-item cleanup goroutine.
func NewMemoryStore(prefix string, opts ...MemoryOption) *MemoryStore {
	s := &MemoryStore{
		items:      make(map[string]*cacheItem),
		prefix:     prefix,
		done:       make(chan bool),
		lockStore:  newMemoryLockStore(),
		maxEntries: DefaultMaxEntries,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// MaxEntries reports the store's entry cap; 0 means unbounded.
func (s *MemoryStore) MaxEntries() int {
	return s.maxEntries
}

// MaxValueBytes reports the store's per-value size cap; 0 means unlimited.
func (s *MemoryStore) MaxValueBytes() int64 {
	return s.maxValueBytes
}

// checkValueSize enforces the optional per-value cap. The memory store holds
// values unserialized, so the size is measured by running the value through
// the package serializer -- only when a cap is configured, keeping the
// default path free of serialization cost. Safe to call without the lock:
// maxValueBytes is immutable after construction.
func (s *MemoryStore) checkValueSize(value interface{}) error {
	if s.maxValueBytes <= 0 {
		return nil
	}
	data, err := MarshalValue(value)
	if err != nil {
		return fmt.Errorf("velocity/cache: cannot measure value against MaxValueBytes cap: %w", err)
	}
	if int64(len(data)) > s.maxValueBytes {
		return fmt.Errorf("velocity/cache: value size %d exceeds maximum of %d bytes: %w", len(data), s.maxValueBytes, ErrValueTooLarge)
	}
	return nil
}

// Start begins the background goroutine that periodically removes expired
// items from the store. Must be called after construction. The goroutine
// is wrapped via async.Go so any panic in the sweep loop is recovered and
// logged via the framework panic handler.
func (s *MemoryStore) Start() {
	s.ticker = time.NewTicker(1 * time.Minute)
	async.Go(func() { s.cleanupExpired() })
}

// cleanupExpired removes expired items periodically
func (s *MemoryStore) cleanupExpired() {
	for {
		select {
		case <-s.ticker.C:
			s.mu.Lock()
			now := time.Now()
			for key, item := range s.items {
				if item.expiration != nil && now.After(*item.expiration) {
					s.removeLocked(key, item)
				}
			}
			s.mu.Unlock()
		case <-s.done:
			return
		}
	}
}

// Shutdown stops the background cleanup goroutine.
// It is safe to call Shutdown more than once (idempotent) and before or
// after Start(). The context is accepted for interface uniformity with
// other ShutdownAware types; memory store cleanup completes promptly so
// the deadline is only consulted when waiting for the ticker stop.
func (s *MemoryStore) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		if s.ticker != nil {
			s.ticker.Stop()
		}
		close(s.done)
	})
	// Honour the context deadline explicitly — callers may shutdown
	// many drivers concurrently, and a cancelled ctx should be
	// reflected.
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// prefixedKey returns the key with prefix.
func (s *MemoryStore) prefixedKey(key string) string {
	return PrefixKey(s.prefix, key)
}

// expirationFor maps a TTL to an absolute expiration pointer. A ttl <= 0
// means "store forever" per the Store contract, so it returns nil (no
// expiration) rather than an instant-in-the-past deadline.
func expirationFor(ttl time.Duration) *time.Time {
	if ttl <= 0 {
		return nil
	}
	exp := time.Now().Add(ttl)
	return &exp
}

// setLocked inserts or replaces the (already prefixed) key while maintaining
// recency and the entry cap. Replacing an existing key never evicts;
// inserting a new key at capacity evicts a sampled least-recently-used entry
// first. Callers must hold the write lock.
func (s *MemoryStore) setLocked(key string, value interface{}, expiration *time.Time) {
	if item, exists := s.items[key]; exists {
		item.value = value
		item.expiration = expiration
		s.touch(item)
		return
	}

	if s.maxEntries > 0 {
		for len(s.items) >= s.maxEntries {
			if !s.evictSampledLocked() {
				break
			}
		}
	}
	item := &cacheItem{value: value, expiration: expiration}
	s.touch(item)
	s.items[key] = item
}

// touch stamps the item with the next access-sequence value, marking it
// most recently used. The stamp is a single atomic store, so callers may
// hold either the read or the write lock. No-op on an unbounded store,
// which never consults recency.
func (s *MemoryStore) touch(item *cacheItem) {
	if s.maxEntries > 0 {
		item.lastAccess.Store(s.accessSeq.Add(1))
	}
}

// evictSampledLocked removes an approximately least-recently-used entry,
// reporting whether anything was evicted. It examines up to
// evictionSampleSize entries (Go map iteration order is randomized, so this
// is a cheap random sample), evicting the first already-expired entry it
// sees, otherwise the sampled entry with the oldest access stamp. When the
// store holds no more entries than the sample size this is exact LRU.
// Callers must hold the write lock.
func (s *MemoryStore) evictSampledLocked() bool {
	var (
		victimKey string
		victimSeq uint64
		found     bool
	)
	now := time.Now()
	examined := 0
	for key, item := range s.items {
		if item.expiration != nil && now.After(*item.expiration) {
			delete(s.items, key)
			return true
		}
		if seq := item.lastAccess.Load(); !found || seq < victimSeq {
			victimKey, victimSeq, found = key, seq, true
		}
		examined++
		if examined >= evictionSampleSize {
			break
		}
	}
	if !found {
		return false
	}
	delete(s.items, victimKey)
	return true
}

// removeLocked deletes the (already prefixed) key.
// Callers must hold the write lock.
func (s *MemoryStore) removeLocked(key string, item *cacheItem) {
	_ = item
	delete(s.items, key)
}

// GetCtx retrieves a value from the cache. The memory store does no I/O so
// ctx is accepted for interface symmetry but otherwise unused.
//
// Hits refresh the key's recency with an atomic stamp, so reads stay on the
// shared read lock and run concurrently on bounded and unbounded stores
// alike.
func (s *MemoryStore) GetCtx(ctx context.Context, key string) (interface{}, bool) {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

	item, exists := s.items[s.prefixedKey(key)]
	if !exists {
		return nil, false
	}

	// Check expiration
	if item.expiration != nil && time.Now().After(*item.expiration) {
		return nil, false
	}

	s.touch(item)
	return item.value, true
}

// Get retrieves a value from the cache.
//
// Deprecated: use GetCtx with a request-scoped context.Context.
func (s *MemoryStore) Get(key string) (interface{}, bool) {
	return s.GetCtx(context.Background(), key)
}

// GetStringCtx retrieves a string value from the cache.
func (s *MemoryStore) GetStringCtx(ctx context.Context, key string) (string, bool) {
	val, found := s.GetCtx(ctx, key)
	if !found {
		return "", false
	}
	str, ok := val.(string)
	if !ok {
		return "", false
	}
	return str, true
}

// GetString retrieves a string value from the cache.
//
// Deprecated: use GetStringCtx with a request-scoped context.Context.
func (s *MemoryStore) GetString(key string) (string, bool) {
	return s.GetStringCtx(context.Background(), key)
}

// PutCtx stores a value in the cache with a TTL.
func (s *MemoryStore) PutCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	_ = ctx
	if err := s.checkValueSize(value); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// ttl <= 0 means store forever (nil expiration), matching ForeverCtx.
	// Computing time.Now().Add(ttl) unconditionally would make ttl=0 write
	// an already-expired entry.
	s.setLocked(s.prefixedKey(key), value, expirationFor(ttl))

	return nil
}

// Put stores a value in the cache with a TTL.
//
// Deprecated: use PutCtx with a request-scoped context.Context.
func (s *MemoryStore) Put(key string, value interface{}, ttl time.Duration) error {
	return s.PutCtx(context.Background(), key, value, ttl)
}

// AddCtx atomically stores a value only if the key does not already exist.
// Returns true if inserted, false on contention (key already present).
// Expired entries are treated as absent and overwritten. The atomic
// check-and-set runs under the store's existing mutex so concurrent
// callers cannot race past the existence check.
func (s *MemoryStore) AddCtx(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	_ = ctx
	// Size check runs before the existence check (matching the file driver,
	// which marshals up-front) so an oversized Add fails uniformly across
	// drivers regardless of whether the key is already present.
	if err := s.checkValueSize(value); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	prefixedKey := s.prefixedKey(key)
	if existing, exists := s.items[prefixedKey]; exists {
		if existing.expiration == nil || time.Now().Before(*existing.expiration) {
			return false, nil
		}
	}

	// ttl <= 0 means store forever (nil expiration), so Add with ttl=0
	// inserts a retrievable entry rather than an already-expired one.
	s.setLocked(prefixedKey, value, expirationFor(ttl))
	return true, nil
}

// Add atomically stores a value only if the key does not already exist.
//
// Deprecated: use AddCtx with a request-scoped context.Context.
func (s *MemoryStore) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	return s.AddCtx(context.Background(), key, value, ttl)
}

// ForeverCtx stores a value in the cache indefinitely. "Forever" means the
// entry never expires, not that it can never leave the store: on a bounded
// store it is still subject to LRU eviction when the cap is reached, and a
// cache must always tolerate entries disappearing.
func (s *MemoryStore) ForeverCtx(ctx context.Context, key string, value interface{}) error {
	_ = ctx
	if err := s.checkValueSize(value); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.setLocked(s.prefixedKey(key), value, nil)

	return nil
}

// Forever stores a value in the cache indefinitely.
//
// Deprecated: use ForeverCtx with a request-scoped context.Context.
func (s *MemoryStore) Forever(key string, value interface{}) error {
	return s.ForeverCtx(context.Background(), key, value)
}

// ForgetCtx removes a value from the cache.
func (s *MemoryStore) ForgetCtx(ctx context.Context, key string) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	prefixedKey := s.prefixedKey(key)
	if item, exists := s.items[prefixedKey]; exists {
		s.removeLocked(prefixedKey, item)
	}
	return nil
}

// Forget removes a value from the cache.
//
// Deprecated: use ForgetCtx with a request-scoped context.Context.
func (s *MemoryStore) Forget(key string) error {
	return s.ForgetCtx(context.Background(), key)
}

// FlushCtx removes all values from the cache.
func (s *MemoryStore) FlushCtx(ctx context.Context) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = make(map[string]*cacheItem)
	return nil
}

// Flush removes all values from the cache.
//
// Deprecated: use FlushCtx with a request-scoped context.Context.
func (s *MemoryStore) Flush() error {
	return s.FlushCtx(context.Background())
}

// IncrementCtx increments a numeric value.
func (s *MemoryStore) IncrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()

	prefixedKey := s.prefixedKey(key)
	item, exists := s.items[prefixedKey]

	// An entry only counts as a base for the increment if it is live; an
	// expired entry is treated as absent (current=0). Capture liveness once
	// so the write-back below does not resurrect a past deadline.
	live := exists && (item.expiration == nil || time.Now().Before(*item.expiration))

	var current int64
	if live {
		switch v := item.value.(type) {
		case int64:
			current = v
		case int:
			current = int64(v)
		case float64:
			current = int64(v)
		default:
			return 0, fmt.Errorf("velocity/cache: value is not numeric")
		}
	}

	newValue := current + value

	// Preserve the expiration only for a live entry. An expired entry starts
	// fresh with no expiration -- reusing item.expiration would write the
	// counter under an already-past deadline, leaving it unreadable. Matches
	// FileStore.IncrementCtx, where expired entries fall through with a nil
	// expiration.
	var expiration *time.Time
	if live {
		expiration = item.expiration
	}
	s.setLocked(prefixedKey, newValue, expiration)

	return newValue, nil
}

// Increment increments a numeric value.
//
// Deprecated: use IncrementCtx with a request-scoped context.Context.
func (s *MemoryStore) Increment(key string, value int64) (int64, error) {
	return s.IncrementCtx(context.Background(), key, value)
}

// DecrementCtx decrements a numeric value.
func (s *MemoryStore) DecrementCtx(ctx context.Context, key string, value int64) (int64, error) {
	return s.IncrementCtx(ctx, key, -value)
}

// Decrement decrements a numeric value.
//
// Deprecated: use DecrementCtx with a request-scoped context.Context.
func (s *MemoryStore) Decrement(key string, value int64) (int64, error) {
	return s.DecrementCtx(context.Background(), key, value)
}

// Remember gets from cache or computes and stores.
func (s *MemoryStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *MemoryStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return RememberForeverFrom(s, s, key, callback)
}

// ManyCtx retrieves multiple values. Like GetCtx, hits refresh recency via
// atomic stamps, so the shared read lock suffices.
func (s *MemoryStore) ManyCtx(ctx context.Context, keys []string) map[string]interface{} {
	_ = ctx
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]interface{}, len(keys))
	now := time.Now()

	for _, key := range keys {
		item, exists := s.items[s.prefixedKey(key)]
		if exists && (item.expiration == nil || now.Before(*item.expiration)) {
			s.touch(item)
			result[key] = item.value
		}
	}

	return result
}

// Many retrieves multiple values.
//
// Deprecated: use ManyCtx with a request-scoped context.Context.
func (s *MemoryStore) Many(keys []string) map[string]interface{} {
	return s.ManyCtx(context.Background(), keys)
}

// PutManyCtx stores multiple values.
// Computes expiration inside the per-item loop so each stored entry carries
// a pointer to its own *time.Time. The previous implementation shared one
// *time.Time across every item in the batch, which meant later Increment
// calls (which preserve the pointer) could extend TTLs unexpectedly, and
// produced surprising behaviour if the loop body grew to compute per-item
// expirations.
func (s *MemoryStore) PutManyCtx(ctx context.Context, items map[string]interface{}, ttl time.Duration) error {
	_ = ctx
	// Validate sizes before storing anything so an oversized batch fails
	// atomically instead of leaving a partial write behind.
	for _, value := range items {
		if err := s.checkValueSize(value); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, value := range items {
		s.setLocked(s.prefixedKey(key), value, expirationFor(ttl))
	}

	return nil
}

// PutMany stores multiple values.
//
// Deprecated: use PutManyCtx with a request-scoped context.Context.
func (s *MemoryStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	return s.PutManyCtx(context.Background(), items, ttl)
}

// HasCtx checks if a key exists.
func (s *MemoryStore) HasCtx(ctx context.Context, key string) bool {
	_, found := s.GetCtx(ctx, key)
	return found
}

// Has checks if a key exists.
//
// Deprecated: use HasCtx with a request-scoped context.Context.
func (s *MemoryStore) Has(key string) bool {
	return s.HasCtx(context.Background(), key)
}

// GetPrefix returns the cache prefix
func (s *MemoryStore) GetPrefix() string {
	return s.prefix
}
