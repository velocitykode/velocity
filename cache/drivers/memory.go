package drivers

import (
	"container/list"
	"context"
	"fmt"
	"sync"
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
// cap, the least-recently-used entry is evicted. Eviction is exact LRU via a
// doubly-linked recency list (O(1) per operation) rather than sampled-random
// eviction: the per-entry overhead is one list element, and exact LRU keeps
// hot entries deterministically resident. The trade-off is that reads on a
// bounded store take the write lock, because a cache hit moves the entry to
// the front of the recency list.
type MemoryStore struct {
	mu        sync.RWMutex
	items     map[string]*cacheItem
	lru       *list.List // most-recently-used at front; nil when unbounded
	prefix    string
	ticker    *time.Ticker
	done      chan bool
	closeOnce sync.Once
	lockStore *memoryLockStore

	// maxEntries is the resolved entry cap; 0 means unbounded.
	maxEntries int

	// maxValueBytes caps the serialized size of a single value accepted by
	// Put/Add/Forever; 0 means unlimited. Immutable after construction.
	maxValueBytes int64
}

// cacheItem represents a cached item with expiration
type cacheItem struct {
	value      interface{}
	expiration *time.Time
	elem       *list.Element // position in the LRU list; nil when unbounded
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
	if s.maxEntries > 0 {
		s.lru = list.New()
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

// setLocked inserts or replaces the (already prefixed) key while maintaining
// the LRU order and the entry cap. Replacing an existing key never evicts;
// inserting a new key at capacity evicts the least-recently-used entry first.
// Callers must hold the write lock.
func (s *MemoryStore) setLocked(key string, value interface{}, expiration *time.Time) {
	if item, exists := s.items[key]; exists {
		item.value = value
		item.expiration = expiration
		s.touchLocked(item)
		return
	}

	item := &cacheItem{value: value, expiration: expiration}
	if s.maxEntries > 0 {
		for len(s.items) >= s.maxEntries {
			if !s.evictLRULocked() {
				break
			}
		}
		item.elem = s.lru.PushFront(key)
	}
	s.items[key] = item
}

// touchLocked moves the item to the front of the LRU list. No-op on an
// unbounded store. Callers must hold the write lock.
func (s *MemoryStore) touchLocked(item *cacheItem) {
	if item.elem != nil {
		s.lru.MoveToFront(item.elem)
	}
}

// evictLRULocked removes the least-recently-used entry, reporting whether
// anything was evicted. Callers must hold the write lock.
func (s *MemoryStore) evictLRULocked() bool {
	back := s.lru.Back()
	if back == nil {
		return false
	}
	delete(s.items, back.Value.(string))
	s.lru.Remove(back)
	return true
}

// removeLocked deletes the (already prefixed) key and its LRU list element.
// Callers must hold the write lock.
func (s *MemoryStore) removeLocked(key string, item *cacheItem) {
	if item.elem != nil {
		s.lru.Remove(item.elem)
	}
	delete(s.items, key)
}

// GetCtx retrieves a value from the cache. The memory store does no I/O so
// ctx is accepted for interface symmetry but otherwise unused.
//
// On a bounded store a hit refreshes the key's LRU recency, which mutates the
// recency list, so the write lock is taken; unbounded stores keep the shared
// read lock.
func (s *MemoryStore) GetCtx(ctx context.Context, key string) (interface{}, bool) {
	_ = ctx
	if s.maxEntries > 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
	} else {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}

	item, exists := s.items[s.prefixedKey(key)]
	if !exists {
		return nil, false
	}

	// Check expiration
	if item.expiration != nil && time.Now().After(*item.expiration) {
		return nil, false
	}

	s.touchLocked(item)
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

	expiration := time.Now().Add(ttl)
	s.setLocked(s.prefixedKey(key), value, &expiration)

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

	expiration := time.Now().Add(ttl)
	s.setLocked(prefixedKey, value, &expiration)
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
	if s.lru != nil {
		s.lru.Init()
	}
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

	var current int64
	if exists && (item.expiration == nil || time.Now().Before(*item.expiration)) {
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

	// Preserve expiration if it exists
	if exists && item.expiration != nil {
		s.setLocked(prefixedKey, newValue, item.expiration)
	} else {
		s.setLocked(prefixedKey, newValue, nil)
	}

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

// ManyCtx retrieves multiple values. Like GetCtx, hits on a bounded store
// refresh LRU recency, so the write lock is taken there.
func (s *MemoryStore) ManyCtx(ctx context.Context, keys []string) map[string]interface{} {
	_ = ctx
	if s.maxEntries > 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
	} else {
		s.mu.RLock()
		defer s.mu.RUnlock()
	}

	result := make(map[string]interface{}, len(keys))
	now := time.Now()

	for _, key := range keys {
		item, exists := s.items[s.prefixedKey(key)]
		if exists && (item.expiration == nil || now.Before(*item.expiration)) {
			s.touchLocked(item)
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
		expiration := time.Now().Add(ttl)
		s.setLocked(s.prefixedKey(key), value, &expiration)
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
