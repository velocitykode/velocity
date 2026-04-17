package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/cache/drivers"
)

// CacheManager is the interface satisfied by *Manager. It covers the methods
// used through app.Services and router.Context for cache operations,
// store management, locking, and event wiring.
type CacheManager interface {
	// Basic operations on the default store.
	Get(key string) (interface{}, bool)
	GetWithContext(ctx context.Context, key string) (interface{}, bool)
	GetString(key string) (string, bool)
	Put(key string, value interface{}, ttl time.Duration) error
	PutWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Forever(key string, value interface{}) error
	ForeverWithContext(ctx context.Context, key string, value interface{}) error
	Forget(key string) error
	ForgetWithContext(ctx context.Context, key string) error
	Flush() error
	Has(key string) bool
	Increment(key string, value int64) (int64, error)
	Decrement(key string, value int64) (int64, error)
	Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error)
	RememberForever(key string, callback func() interface{}) (interface{}, error)
	Many(keys []string) map[string]interface{}
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// Store management.
	Store(name string) (Store, error)
	DefaultStore() (Store, error)
	Close() error

	// Distributed locking.
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock

	// Event wiring.
	SetEventDispatcher(fn func(event interface{}) error)
}

// Verify *Manager implements CacheManager at compile time.
var _ CacheManager = (*Manager)(nil)

// Manager manages multiple cache stores
type Manager struct {
	mu              sync.RWMutex
	stores          map[string]Store
	defaultStore    string
	config          *Config
	eventDispatcher func(event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events.
// This is called by the events package to wire up event dispatching.
func (m *Manager) SetEventDispatcher(fn func(event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *Manager) dispatchEvent(event interface{}) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// Config holds cache configuration
type Config struct {
	Default string
	Stores  map[string]StoreConfig
	Prefix  string
}

// StoreConfig holds configuration for a specific store
type StoreConfig struct {
	Driver string
	Prefix string
	// Additional driver-specific config can be added here
	Path     string // For file driver
	Host     string // For Redis driver
	Port     int    // For Redis driver
	Password string // For Redis driver
	Database int    // For Redis driver
	Table    string // For database driver
	TLS      bool   // Enable TLS for Redis connections
}

// DefaultConfig returns a Config with sensible defaults (single in-memory store).
func DefaultConfig() *Config {
	return &Config{
		Default: "default",
		Prefix:  "velocity_cache",
		Stores: map[string]StoreConfig{
			"default": {Driver: "memory"},
		},
	}
}

// Validate checks that the Config is internally consistent.
func (c *Config) Validate() error {
	if c.Default == "" {
		return fmt.Errorf("cache: default store name is required")
	}
	if len(c.Stores) == 0 {
		return fmt.Errorf("cache: at least one store must be configured")
	}
	if _, exists := c.Stores[c.Default]; !exists {
		return fmt.Errorf("cache: default store %q not found in configured stores", c.Default)
	}
	return nil
}

// NewManager creates a new cache manager with lazy store initialization.
func NewManager(config *Config) *Manager {
	return &Manager{
		stores:       make(map[string]Store),
		defaultStore: config.Default,
		config:       config,
	}
}

// Store returns a cache store by name
func (m *Manager) Store(name string) (Store, error) {
	m.mu.RLock()
	store, exists := m.stores[name]
	m.mu.RUnlock()

	if exists {
		return store, nil
	}

	// Create store if it doesn't exist
	return m.createStore(name)
}

// createStore creates a new cache store
func (m *Manager) createStore(name string) (Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check again in case another goroutine created it
	if store, exists := m.stores[name]; exists {
		return store, nil
	}

	config, exists := m.config.Stores[name]
	if !exists {
		return nil, fmt.Errorf("cache store '%s' is not configured", name)
	}

	// Combine global and store-specific prefix
	prefix := m.config.Prefix
	if config.Prefix != "" {
		if prefix != "" {
			prefix = prefix + ":" + config.Prefix
		} else {
			prefix = config.Prefix
		}
	}

	var store Store
	var err error

	switch config.Driver {
	case DriverMemory:
		store = drivers.NewMemoryStore(prefix)
	case DriverFile:
		store, err = drivers.NewFileStore(prefix, config.Path)
	case DriverRedis:
		store, err = drivers.NewRedisStore(prefix, config.Host, config.Port, config.Password, config.Database, config.TLS)
	case DriverDatabase:
		// TODO: Implement database driver
		return nil, fmt.Errorf("database driver not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported cache driver: %s", config.Driver)
	}

	if err != nil {
		return nil, err
	}

	if starter, ok := store.(interface{ Start() }); ok {
		starter.Start()
	}

	m.stores[name] = store
	return store, nil
}

// DefaultStore returns the default cache store
func (m *Manager) DefaultStore() (Store, error) {
	return m.Store(m.defaultStore)
}

// Close closes all cache stores
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, store := range m.stores {
		if closer, ok := store.(interface{ Close() error }); ok {
			closer.Close()
		}
	}

	m.stores = make(map[string]Store)
	return nil
}

// Implementation of Cache interface for default store

// Get retrieves a value from the default cache store
func (m *Manager) Get(key string) (interface{}, bool) {
	return m.GetWithContext(context.Background(), key)
}

// GetWithContext retrieves a value from the default cache store with context
func (m *Manager) GetWithContext(ctx context.Context, key string) (interface{}, bool) {
	store, err := m.DefaultStore()
	if err != nil {
		return nil, false
	}
	value, found := store.Get(key)
	if found {
		m.dispatchCacheHit(ctx, key, m.defaultStore)
	} else {
		m.dispatchCacheMiss(ctx, key, m.defaultStore)
	}
	return value, found
}

// GetString retrieves a string value from the default cache store
func (m *Manager) GetString(key string) (string, bool) {
	store, err := m.DefaultStore()
	if err != nil {
		return "", false
	}
	return store.GetString(key)
}

// Put stores a value in the default cache store
func (m *Manager) Put(key string, value interface{}, ttl time.Duration) error {
	return m.PutWithContext(context.Background(), key, value, ttl)
}

// PutWithContext stores a value in the default cache store with context
func (m *Manager) PutWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	store, err := m.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Put(key, value, ttl); err != nil {
		return err
	}
	m.dispatchCacheWritten(ctx, key, m.defaultStore, ttl)
	return nil
}

// Forever stores a value in the default cache store indefinitely
func (m *Manager) Forever(key string, value interface{}) error {
	return m.ForeverWithContext(context.Background(), key, value)
}

// ForeverWithContext stores a value in the default cache store indefinitely with context
func (m *Manager) ForeverWithContext(ctx context.Context, key string, value interface{}) error {
	store, err := m.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Forever(key, value); err != nil {
		return err
	}
	m.dispatchCacheWritten(ctx, key, m.defaultStore, 0) // TTL=0 means forever
	return nil
}

// Forget removes a value from the default cache store
func (m *Manager) Forget(key string) error {
	return m.ForgetWithContext(context.Background(), key)
}

// ForgetWithContext removes a value from the default cache store with context
func (m *Manager) ForgetWithContext(ctx context.Context, key string) error {
	store, err := m.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Forget(key); err != nil {
		return err
	}
	m.dispatchCacheForgotten(ctx, key, m.defaultStore)
	return nil
}

// Flush removes all values from the default cache store
func (m *Manager) Flush() error {
	store, err := m.DefaultStore()
	if err != nil {
		return err
	}
	return store.Flush()
}

// Increment increments a numeric value in the default cache store
func (m *Manager) Increment(key string, value int64) (int64, error) {
	store, err := m.DefaultStore()
	if err != nil {
		return 0, err
	}
	return store.Increment(key, value)
}

// Decrement decrements a numeric value in the default cache store
func (m *Manager) Decrement(key string, value int64) (int64, error) {
	store, err := m.DefaultStore()
	if err != nil {
		return 0, err
	}
	return store.Decrement(key, value)
}

// Remember gets from default cache or computes and stores
func (m *Manager) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	store, err := m.DefaultStore()
	if err != nil {
		return nil, err
	}
	return store.Remember(key, ttl, callback)
}

// RememberForever gets from default cache or computes and stores forever
func (m *Manager) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	store, err := m.DefaultStore()
	if err != nil {
		return nil, err
	}
	return store.RememberForever(key, callback)
}

// Many retrieves multiple values from the default cache store
func (m *Manager) Many(keys []string) map[string]interface{} {
	store, err := m.DefaultStore()
	if err != nil {
		return make(map[string]interface{})
	}
	return store.Many(keys)
}

// PutMany stores multiple values in the default cache store
func (m *Manager) PutMany(items map[string]interface{}, ttl time.Duration) error {
	store, err := m.DefaultStore()
	if err != nil {
		return err
	}
	return store.PutMany(items, ttl)
}

// Has checks if a key exists in the default cache store
func (m *Manager) Has(key string) bool {
	store, err := m.DefaultStore()
	if err != nil {
		return false
	}
	return store.Has(key)
}

// Lock creates a new lock for the given key on the default store.
// An optional TTL auto-expires the lock to prevent deadlocks if the holder crashes.
// Returns nil if the default store does not support locking.
func (m *Manager) Lock(key string, ttl ...time.Duration) Lock {
	store, err := m.DefaultStore()
	if err != nil {
		return nil
	}

	if locker, ok := store.(drivers.Locker); ok {
		return locker.Lock(key, ttl...)
	}
	return nil
}

// RestoreLock restores an existing lock by key and owner token on the default store.
// Returns nil if the default store does not support locking.
func (m *Manager) RestoreLock(key string, owner string) Lock {
	store, err := m.DefaultStore()
	if err != nil {
		return nil
	}

	if locker, ok := store.(drivers.Locker); ok {
		return locker.RestoreLock(key, owner)
	}
	return nil
}
