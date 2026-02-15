package drivers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// FileStore implements a file-based cache store
type FileStore struct {
	mu     sync.RWMutex
	path   string
	prefix string
	done   chan struct{}
}

// fileCacheItem represents a cached item stored in file
type fileCacheItem struct {
	Value      json.RawMessage `json:"value"`
	Expiration *time.Time      `json:"expiration,omitempty"`
}

// NewFileStore creates a new file cache store
func NewFileStore(prefix, path string) (*FileStore, error) {
	if path == "" {
		path = "storage/framework/cache/data"
	}

	// Create cache directory if it doesn't exist
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	store := &FileStore{
		path:   path,
		prefix: prefix,
		done:   make(chan struct{}),
	}

	// Start cleanup goroutine
	go store.cleanupExpired()

	return store, nil
}

// Close stops the background cleanup goroutine.
func (s *FileStore) Close() error {
	close(s.done)
	return nil
}

// cleanupExpired removes expired cache files periodically.
// It stops when the done channel is closed via Close().
func (s *FileStore) cleanupExpired() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			filepath.Walk(s.path, func(path string, info os.FileInfo, err error) error {
				if err != nil || info.IsDir() {
					return nil
				}

				// Read file to check expiration
				data, err := os.ReadFile(path)
				if err != nil {
					return nil
				}

				var item fileCacheItem
				if err := json.Unmarshal(data, &item); err != nil {
					return nil
				}

				// Remove if expired
				if item.Expiration != nil && time.Now().After(*item.Expiration) {
					os.Remove(path)
				}

				return nil
			})
			s.mu.Unlock()
		}
	}
}

// getCacheFilePath returns the file path for a cache key
func (s *FileStore) getCacheFilePath(key string) string {
	// Create a hash of the key for the filename
	hasher := sha256.New()
	hasher.Write([]byte(s.prefixedKey(key)))
	hash := hex.EncodeToString(hasher.Sum(nil))

	// Use first 2 characters for directory structure
	dir := filepath.Join(s.path, hash[:2])
	os.MkdirAll(dir, 0700)

	return filepath.Join(dir, hash)
}

// prefixedKey returns the key with prefix.
func (s *FileStore) prefixedKey(key string) string {
	return PrefixKey(s.prefix, key)
}

// Get retrieves a value from the cache
func (s *FileStore) Get(key string) (interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.getCacheFilePath(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var item fileCacheItem
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, false
	}

	// Check expiration
	if item.Expiration != nil && time.Now().After(*item.Expiration) {
		os.Remove(path)
		return nil, false
	}

	// Unmarshal the actual value
	var value interface{}
	if err := json.Unmarshal(item.Value, &value); err != nil {
		return nil, false
	}

	return value, true
}

// GetString retrieves a string value from the cache.
func (s *FileStore) GetString(key string) (string, bool) {
	return GetStringFrom(s, key)
}

// Put stores a value in the cache with a TTL
func (s *FileStore) Put(key string, value interface{}, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal the value
	valueData, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	expiration := time.Now().Add(ttl)
	item := fileCacheItem{
		Value:      valueData,
		Expiration: &expiration,
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Forever stores a value in the cache indefinitely
func (s *FileStore) Forever(key string, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal the value
	valueData, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal value: %w", err)
	}

	item := fileCacheItem{
		Value:      valueData,
		Expiration: nil,
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write cache file: %w", err)
	}

	return nil
}

// Forget removes a value from the cache
func (s *FileStore) Forget(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Flush removes all values from the cache
func (s *FileStore) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove all files in cache directory
	return filepath.Walk(s.path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return os.Remove(path)
		}
		return nil
	})
}

// Increment increments a numeric value
func (s *FileStore) Increment(key string, value int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var current int64
	var expiration *time.Time

	// Try to get current value
	path := s.getCacheFilePath(key)
	if data, err := os.ReadFile(path); err == nil {
		var item fileCacheItem
		if err := json.Unmarshal(data, &item); err == nil {
			// Check expiration
			if item.Expiration == nil || time.Now().Before(*item.Expiration) {
				var val interface{}
				if err := json.Unmarshal(item.Value, &val); err == nil {
					switch v := val.(type) {
					case float64:
						current = int64(v)
					case int64:
						current = v
					case int:
						current = int64(v)
					}
				}
				expiration = item.Expiration
			}
		}
	}

	newValue := current + value

	// Marshal the new value
	valueData, err := json.Marshal(newValue)
	if err != nil {
		return 0, err
	}

	item := fileCacheItem{
		Value:      valueData,
		Expiration: expiration,
	}

	// Marshal and save
	data, err := json.Marshal(item)
	if err != nil {
		return 0, err
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return 0, err
	}

	return newValue, nil
}

// Decrement decrements a numeric value
func (s *FileStore) Decrement(key string, value int64) (int64, error) {
	return s.Increment(key, -value)
}

// Remember gets from cache or computes and stores.
func (s *FileStore) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return RememberFrom(s, s, key, ttl, callback)
}

// RememberForever gets from cache or computes and stores forever.
func (s *FileStore) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return RememberForeverFrom(s, s, key, callback)
}

// Many retrieves multiple values
func (s *FileStore) Many(keys []string) map[string]interface{} {
	result := make(map[string]interface{})
	for _, key := range keys {
		if val, found := s.Get(key); found {
			result[key] = val
		}
	}
	return result
}

// PutMany stores multiple values
func (s *FileStore) PutMany(items map[string]interface{}, ttl time.Duration) error {
	for key, value := range items {
		if err := s.Put(key, value, ttl); err != nil {
			return err
		}
	}
	return nil
}

// Has checks if a key exists.
func (s *FileStore) Has(key string) bool {
	return HasFrom(s, key)
}

// GetPrefix returns the cache prefix
func (s *FileStore) GetPrefix() string {
	return s.prefix
}
