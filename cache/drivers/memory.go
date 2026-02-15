package drivers

import (
	"fmt"
	"sync"
	"time"
)

// MemoryStore implements an in-memory cache store
type MemoryStore struct {
	mu     sync.RWMutex
	items  map[string]*cacheItem
	prefix string
	ticker *time.Ticker
	done   chan bool
}

// cacheItem represents a cached item with expiration
type cacheItem struct {
	value      interface{}
	expiration *time.Time
}

// NewMemoryStore creates a new memory cache store
func NewMemoryStore(prefix string) *MemoryStore {
	store := &MemoryStore{
		items:  make(map[string]*cacheItem),
		prefix: prefix,
		done:   make(chan bool),
	}

	// Start cleanup goroutine
	store.ticker = time.NewTicker(1 * time.Minute)
	go store.cleanupExpired()

	return store
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
					delete(s.items, key)
				}
			}
			s.mu.Unlock()
		case <-s.done:
			return
		}
	}
}

// Close stops the cleanup goroutine
func (s *MemoryStore) Close() {
	s.ticker.Stop()
	close(s.done)
}

// prefixedKey returns the key with prefix.
func (s *MemoryStore) prefixedKey(key string) string {
	return PrefixKey(s.prefix, key)
}

// Get retrieves a value from the cache
func (s *MemoryStore) Get(key string) (interface{}, bool) {
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

	return item.value, true
}

// GetString retrieves a string value from the cache.
func (s *MemoryStore) GetString(key string) (string, bool) {
	return GetStringFrom(s, key)
}

// Put stores a value in the cache with a TTL
func (s *MemoryStore) Put(key string, value interface{}, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiration := time.Now().Add(ttl)
	s.items[s.prefixedKey(key)] = &cacheItem{
		value:      value,
		expiration: &expiration,
	}

	return nil
}

// Forever stores a value in the cache indefinitely
func (s *MemoryStore) Forever(key string, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items[s.prefixedKey(key)] = &cacheItem{
		value:      value,
		expiration: nil,
	}

	return nil
}

// Forget removes a value from the cache
func (s *MemoryStore) Forget(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.items, s.prefixedKey(key))
	return nil
}

// Flush removes all values from the cache
func (s *MemoryStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.items = make(map[string]*cacheItem)
	return nil
}

// Increment increments a numeric value
func (s *MemoryStore) Increment(key string, value int64) (int64, error) {
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
			return 0, fmt.Errorf("value is not numeric")
		}
	}

	newValue := current + value

	// Preserve expiration if it exists
	if exists && item.expiration != nil {
		s.items[prefixedKey] = &cacheItem{
			value:      newValue,
			expiration: item.expiration,
		}
	} else {
		s.items[prefixedKey] = &cacheItem{
			value:      newValue,
			expiration: nil,
		}
	}

	return newValue, nil
}

// Decrement decrements a numeric value
func (s *MemoryStore) Decrement(key string, value int64) (int64, error) {
	return s.Increment(key, -value)
}

// Remember gets from cache or computes and stores.
func (s *MemoryStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *MemoryStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return RememberForeverFrom(s, s, key, callback)
}

// Many retrieves multiple values
func (s *MemoryStore) Many(keys []string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]interface{}, len(keys))
	now := time.Now()

	for _, key := range keys {
		item, exists := s.items[s.prefixedKey(key)]
		if exists && (item.expiration == nil || now.Before(*item.expiration)) {
			result[key] = item.value
		}
	}

	return result
}

// PutMany stores multiple values
func (s *MemoryStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiration := time.Now().Add(ttl)
	for key, value := range items {
		s.items[s.prefixedKey(key)] = &cacheItem{
			value:      value,
			expiration: &expiration,
		}
	}

	return nil
}

// Has checks if a key exists.
func (s *MemoryStore) Has(key string) bool {
	return HasFrom(s, key)
}

// GetPrefix returns the cache prefix
func (s *MemoryStore) GetPrefix() string {
	return s.prefix
}
