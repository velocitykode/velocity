package drivers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/velocitykode/velocity/async"
)

// newAddOwnerID returns a unique owner identifier for the cross-process
// flock used by FileStore.Add when taking over an expired entry. The
// owner string is opaque to callers; it just needs to be unique enough
// for the flock release to recognise its own acquire.
func newAddOwnerID() string {
	return uuid.New().String()
}

// DefaultFileCleanupInterval is the default period between expired-file sweeps.
const DefaultFileCleanupInterval = 5 * time.Minute

// FileStore implements a file-based cache store
type FileStore struct {
	mu              sync.RWMutex
	path            string
	prefix          string
	cleanupInterval time.Duration
	shardDirs       sync.Map // pre-created shard dirs so per-write MkdirAll is avoided
	done            chan struct{}
	closeOnce       sync.Once

	// lockStore and friends back FileStore.Lock with flock(2) so the
	// file driver satisfies the Locker capability. Created lazily on
	// first Lock call; lockErr captures any initialisation failure so
	// subsequent calls don't repeatedly attempt MkdirAll on a broken
	// filesystem.
	lockOnce  sync.Once
	lockStore *fileLockStore
	lockErr   error
}

// fileCacheItem represents a cached item stored in file
type fileCacheItem struct {
	Value      json.RawMessage `json:"value"`
	Expiration *time.Time      `json:"expiration,omitempty"`
}

// NewFileStore creates a new file cache store.
// The cache root directory is created up-front so individual Put calls don't
// have to MkdirAll on every write. Call Start() to begin the background
// expired-item cleanup goroutine.
func NewFileStore(prefix, path string) (*FileStore, error) {
	return NewFileStoreWithOptions(prefix, path, DefaultFileCleanupInterval)
}

// NewFileStoreWithOptions creates a new file cache store with a configurable
// cleanup interval. Pass 0 to use DefaultFileCleanupInterval. This exists so
// tests (and callers that want to tune memory/disk tradeoffs) don't have to
// wait for the 5-minute default.
func NewFileStoreWithOptions(prefix, path string, cleanupInterval time.Duration) (*FileStore, error) {
	if path == "" {
		path = "storage/framework/cache/data"
	}

	// Create cache root up-front. Callers that add new shard directories
	// later (see getCacheFilePath) also MkdirAll, but the common case is
	// covered here and avoids the system call on every Put.
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("velocity/cache: failed to create cache directory: %w", err)
	}

	if cleanupInterval <= 0 {
		cleanupInterval = DefaultFileCleanupInterval
	}

	return &FileStore{
		path:            path,
		prefix:          prefix,
		cleanupInterval: cleanupInterval,
		done:            make(chan struct{}),
	}, nil
}

// Start begins the background goroutine that periodically removes expired
// cache files. Must be called after construction. Wrapped with async.Go so
// any panic in the walker is recovered instead of tearing down the process.
func (s *FileStore) Start() {
	async.Go(func() { s.cleanupExpired() })
}

// Shutdown stops the background cleanup goroutine. Safe to call multiple
// times. Honours the context deadline for uniformity with other
// ShutdownAware types.
func (s *FileStore) Shutdown(ctx context.Context) error {
	s.closeOnce.Do(func() {
		close(s.done)
	})
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// cleanupExpired removes expired cache files periodically.
// It stops when the done channel is closed via Shutdown().
func (s *FileStore) cleanupExpired() {
	ticker := time.NewTicker(s.cleanupInterval)
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

// getCacheFilePath returns the file path for a cache key.
// A 2-char sharded directory is created lazily on first use and cached in
// shardDirs so subsequent writes to the same shard skip the MkdirAll syscall.
func (s *FileStore) getCacheFilePath(key string) string {
	hasher := sha256.New()
	hasher.Write([]byte(s.prefixedKey(key)))
	hash := hex.EncodeToString(hasher.Sum(nil))

	shard := hash[:2]
	dir := filepath.Join(s.path, shard)

	if _, seen := s.shardDirs.Load(shard); !seen {
		// Ignore error: subsequent writes will surface it if the dir is
		// unusable. Cache the shard regardless to avoid repeated syscalls.
		_ = os.MkdirAll(dir, 0700)
		s.shardDirs.Store(shard, struct{}{})
	}

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
		return fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}

	expiration := time.Now().Add(ttl)
	item := fileCacheItem{
		Value:      valueData,
		Expiration: &expiration,
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("velocity/cache: failed to write cache file: %w", err)
	}

	return nil
}

// Add atomically stores a value only if the key does not already
// exist (or its existing entry is expired). Returns true if inserted,
// false if a non-expired entry was already present or if another
// process holds the takeover lock for the same key.
//
// A non-nil error indicates a real backend failure (write or lock
// acquisition); callers must not treat (false, err) as benign
// contention.
//
// Atomicity is layered:
//
//   - Same-process goroutines serialize on the FileStore write mutex.
//   - Cross-process / cross-instance contention is gated by os.O_EXCL
//     for the create path (kernel-enforced single creator) and by an
//     advisory flock(2) under the existing per-key lock infrastructure
//     for the expired-entry takeover path.
//
// On a platform where flock is unavailable (the windows build), the
// expired-entry takeover path returns ErrLockNotSupported instead of
// degrading to last-writer-wins. There is no safe non-flock fallback
// that preserves the SETNX contract; operators that need cross-process
// single-flight on Windows should use the Redis driver. The fresh-key
// create path is still O_EXCL-protected on every platform.
func (s *FileStore) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.getCacheFilePath(key)
	valueData, err := json.Marshal(value)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}
	expiration := time.Now().Add(ttl)
	item := fileCacheItem{
		Value:      valueData,
		Expiration: &expiration,
	}
	data, err := json.Marshal(item)
	if err != nil {
		return false, fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Atomic create-if-absent. O_EXCL is enforced by the kernel; the
	// two-process race that prior best-effort code could lose is closed
	// for the fresh-key path.
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil {
			_ = os.Remove(path)
			return false, fmt.Errorf("velocity/cache: failed to write cache file: %w", werr)
		}
		if cerr != nil {
			return false, fmt.Errorf("velocity/cache: failed to close cache file: %w", cerr)
		}
		return true, nil
	} else if !errors.Is(err, fs.ErrExist) {
		return false, fmt.Errorf("velocity/cache: failed to create cache file: %w", err)
	}

	// File exists. Read it under the same process mutex; if still
	// valid, refuse insertion. If expired, the caller wants to take
	// over - but we must coordinate with other processes that may
	// have made the same observation. Acquire the per-key flock via
	// the existing lock infrastructure, then re-check after lock to
	// avoid the lost-update window.
	if existing, rerr := os.ReadFile(path); rerr == nil {
		// A zero-byte file means another instance just won the O_EXCL
		// create above and has not flushed its payload yet. The kernel
		// has already elected that creator the SETNX winner; we must
		// refuse insertion here. Falling through would race the
		// takeover path against a live creator and let both callers
		// return true (cache/drivers#TestFileStore_Add_CrossInstanceMutualExclusion).
		if len(existing) == 0 {
			return false, nil
		}
		var ex fileCacheItem
		if json.Unmarshal(existing, &ex) == nil {
			if ex.Expiration == nil || time.Now().Before(*ex.Expiration) {
				return false, nil
			}
		}
	}

	// Expired or unparseable entry. Take over under a flock. On
	// platforms where flock(2) is unavailable (windows), there is no
	// safe way to honor the Store.Add SETNX contract for the takeover
	// path - last-writer-wins would let two processes both report
	// successful Add for the same expired key. Surface
	// ErrLockNotSupported instead so the caller knows the driver
	// cannot fulfil the contract on this platform; operators relying
	// on cross-process single-flight should use Redis or run on POSIX.
	lockStore, lerr := s.ensureLockStore()
	if lerr != nil {
		return false, fmt.Errorf("velocity/cache: FileStore.Add cannot fulfil SETNX contract on this platform: %w", lerr)
	}
	owner := newAddOwnerID()
	lock := NewFileLock(lockStore, PrefixKey(s.prefix, "add:"+key), owner, 30*time.Second)
	ctx := context.Background()
	acquired, aerr := lock.GetWithErr(ctx)
	if aerr != nil {
		// Backend failure on the lock store itself (filesystem
		// problem, exhausted file descriptors, etc) is a real error
		// the caller needs to see. Don't mask it as benign
		// contention; Cache.Remember would otherwise poll, then run
		// the populate callback without ever caching the result.
		return false, fmt.Errorf("velocity/cache: FileStore.Add lock acquire failed: %w", aerr)
	}
	if !acquired {
		// Genuine contention: another process holds the takeover
		// lock. Treat as existing entry; caller retries the Get.
		return false, nil
	}
	defer func() { _ = lock.Release(ctx) }()

	// Re-check after acquiring the lock: another worker may have
	// already inserted a fresh entry.
	if existing, rerr := os.ReadFile(path); rerr == nil {
		// Empty file => another O_EXCL creator owns the slot; defer
		// to them just like the pre-lock check does. Without this the
		// takeover write below would clobber a live creator's payload.
		if len(existing) == 0 {
			return false, nil
		}
		var ex fileCacheItem
		if json.Unmarshal(existing, &ex) == nil {
			if ex.Expiration == nil || time.Now().Before(*ex.Expiration) {
				return false, nil
			}
		}
	}

	if werr := os.WriteFile(path, data, 0600); werr != nil {
		return false, fmt.Errorf("velocity/cache: failed to write cache file: %w", werr)
	}
	return true, nil
}

// Forever stores a value in the cache indefinitely
func (s *FileStore) Forever(key string, value interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Marshal the value
	valueData, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal value: %w", err)
	}

	item := fileCacheItem{
		Value:      valueData,
		Expiration: nil,
	}

	// Marshal the cache item
	data, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("velocity/cache: failed to marshal cache item: %w", err)
	}

	// Write to file
	path := s.getCacheFilePath(key)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("velocity/cache: failed to write cache file: %w", err)
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

	// Try to get current value. An existing-but-non-numeric value is an
	// error — silently coercing it to zero (the prior behavior) meant a
	// caller who accidentally Put a string and then Increment'd would see
	// the counter quietly reset, which is exactly the kind of silent
	// corruption integration parity tests are supposed to catch. Match
	// MemoryStore's error message so the parity test asserts one string
	// across drivers.
	path := s.getCacheFilePath(key)
	if data, err := os.ReadFile(path); err == nil {
		var item fileCacheItem
		if err := json.Unmarshal(data, &item); err == nil {
			// Check expiration — expired entries fall through with current=0,
			// same as nonexistent files. Both are legitimate "start from 0" paths.
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
					default:
						return 0, fmt.Errorf("velocity/cache: value is not numeric")
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
