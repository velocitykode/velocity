package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/driverregistry"
)

// driverRegistry is the canonical Velocity driver registry for cache
// stores. Built-in drivers (memory, file, redis) self-register from
// cache/init.go; third-party stores can register additional factories.
var driverRegistry = driverregistry.New[Store, StoreConfig]("cache")

// Drivers returns the registry that cache store factories register
// themselves into. Use this from a driver package's init() to install a
// factory:
//
//	func init() {
//	    cache.Drivers().Register("dragonfly", func(ctx context.Context, cfg cache.StoreConfig) (cache.Store, error) {
//	        return newDragonflyStore(cfg), nil
//	    })
//	}
func Drivers() *driverregistry.Registry[Store, StoreConfig] { return driverRegistry }

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
	Add(key string, value interface{}, ttl time.Duration) (bool, error)
	AddWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error)
	Forever(key string, value interface{}) error
	ForeverWithContext(ctx context.Context, key string, value interface{}) error
	Forget(key string) error
	ForgetWithContext(ctx context.Context, key string) error
	Flush() error
	Has(key string) bool
	Increment(key string, value int64) (int64, error)
	Decrement(key string, value int64) (int64, error)
	Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error)
	RememberWithContext(ctx context.Context, key string, ttl time.Duration, callback func() interface{}) (interface{}, error)
	RememberE(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
	RememberEWithContext(ctx context.Context, key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error)
	RememberForever(key string, callback func() interface{}) (interface{}, error)
	RememberForeverWithContext(ctx context.Context, key string, callback func() interface{}) (interface{}, error)
	RememberForeverE(key string, callback func() (interface{}, error)) (interface{}, error)
	RememberForeverEWithContext(ctx context.Context, key string, callback func() (interface{}, error)) (interface{}, error)
	Many(keys []string) map[string]interface{}
	PutMany(items map[string]interface{}, ttl time.Duration) error

	// Store management.
	Store(name string) (Store, error)
	StoreWithContext(ctx context.Context, name string) (Store, error)
	DefaultStore() (Store, error)
	DefaultStoreWithContext(ctx context.Context) (Store, error)
	Shutdown(ctx context.Context) error

	// Distributed locking.
	Lock(key string, ttl ...time.Duration) Lock
	RestoreLock(key string, owner string) Lock

	// Event wiring.
	SetEventDispatcher(fn func(ctx context.Context, event interface{}) error)
}

// Verify *Manager implements CacheManager at compile time.
var _ CacheManager = (*Manager)(nil)

// Verify *drivers.RedisStore implements ContextStore at compile time so the
// Manager's optional-interface assertion picks it up for ctx-aware operations.
var _ ContextStore = (*drivers.RedisStore)(nil)

// Manager manages multiple cache stores
type Manager struct {
	mu              sync.RWMutex
	stores          map[string]Store
	defaultStore    string
	config          *Config
	eventDispatcher func(ctx context.Context, event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events.
// This is called by the events package to wire up event dispatching.
func (m *Manager) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values.
func (m *Manager) dispatchEvent(ctx context.Context, event interface{}) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		fn(ctx, event)
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
	TLS      bool   // Enable TLS for Redis connections
}

// Validate checks that a driver name is present. Per-driver field validation
// (host/port for redis, path for file, ...) lives in each driver's factory so
// third-party drivers registered via Drivers().Register can enforce their own
// invariants without StoreConfig.Validate having to know about them.
//
// Validate intentionally does NOT consult Drivers().Names() to verify the
// driver is registered: Validate runs at config-load time, and a driver
// package whose init() registers a factory may not have been imported yet at
// that point. Driver-name lookup is the registry's job; Resolve returns a
// typed *driverregistry.NotFoundError if the name is unknown when the store
// is actually constructed.
func (sc StoreConfig) Validate() error {
	if sc.Driver == "" {
		return fmt.Errorf("velocity/cache: driver name required")
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

// Store returns a cache store by name. If the store has not yet been
// instantiated this falls back to context.Background; callers that need to
// honour a request deadline during the driver's connect step (e.g. Redis
// dial) should use StoreWithContext instead.
func (m *Manager) Store(name string) (Store, error) {
	return m.StoreWithContext(context.Background(), name)
}

// StoreWithContext returns a cache store by name, threading the caller's
// ctx through to the driver factory the first time the store is created.
// Subsequent calls hit the cached instance and ignore ctx (the dial has
// already happened); callers do not need to pass the same ctx every time.
func (m *Manager) StoreWithContext(ctx context.Context, name string) (Store, error) {
	m.mu.RLock()
	store, exists := m.stores[name]
	m.mu.RUnlock()

	if exists {
		return store, nil
	}

	// Create store if it doesn't exist
	return m.createStore(ctx, name)
}

// createStore creates a new cache store, validating its per-store config
// before instantiation so misconfigurations fail fast rather than surfacing
// as opaque connection errors later. ctx is forwarded to the driver factory
// so a misconfigured remote driver (e.g. unreachable Redis) fails under the
// caller's deadline rather than a hardcoded background context.
func (m *Manager) createStore(ctx context.Context, name string) (Store, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check again in case another goroutine created it
	if store, exists := m.stores[name]; exists {
		return store, nil
	}

	config, exists := m.config.Stores[name]
	if !exists {
		return nil, fmt.Errorf("velocity/cache: store %q not configured: %w", name, ErrStoreNotFound)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("velocity/cache: store %q invalid: %w", name, err)
	}

	// Combine global and store-specific prefix; mutate a copy of the
	// per-store config so the registry-resolved factory sees the merged
	// prefix without the manager mutating the user-supplied Config.
	resolved := config
	prefix := m.config.Prefix
	if config.Prefix != "" {
		if prefix != "" {
			prefix = prefix + ":" + config.Prefix
		} else {
			prefix = config.Prefix
		}
	}
	resolved.Prefix = prefix

	store, err := driverRegistry.Resolve(ctx, config.Driver, resolved)
	if err != nil {
		return nil, fmt.Errorf("velocity/cache: store %q: %w", name, err)
	}

	if starter, ok := store.(interface{ Start() }); ok {
		starter.Start()
	}

	m.stores[name] = store
	return store, nil
}

// DefaultStore returns the default cache store. See Store for the ctx
// caveat: callers that need to honour a request deadline during first
// instantiation should use DefaultStoreWithContext.
func (m *Manager) DefaultStore() (Store, error) {
	return m.StoreWithContext(context.Background(), m.defaultStore)
}

// DefaultStoreWithContext returns the default cache store, threading ctx
// through to the driver factory the first time the store is created.
func (m *Manager) DefaultStoreWithContext(ctx context.Context) (Store, error) {
	return m.StoreWithContext(ctx, m.defaultStore)
}

// Shutdown closes all cache stores, honoring the context deadline. All
// built-in stores implement ShutdownAware; unknown types are ignored.
// Each ShutdownAware store gets a Shutdown attempt even if a previous one
// fails; errors are collected per-store and returned joined via
// errors.Join. The internal store map is cleared regardless of errors so
// callers cannot accidentally reuse a half-torn-down Manager.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, store := range m.stores {
		if sd, ok := store.(contract.ShutdownAware); ok {
			if err := sd.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("cache store %q shutdown: %w", name, err))
			}
		}
	}

	m.stores = make(map[string]Store)
	return errors.Join(errs...)
}

// Implementation of Cache interface for default store

// Get retrieves a value from the default cache store
func (m *Manager) Get(key string) (interface{}, bool) {
	return m.GetWithContext(context.Background(), key)
}

// GetWithContext retrieves a value from the default cache store with context.
// When the underlying store implements ContextStore the ctx is threaded through
// to the driver so remote lookups (e.g. Redis) can be cancelled on request
// cancellation; otherwise falls back to the plain Get.
func (m *Manager) GetWithContext(ctx context.Context, key string) (interface{}, bool) {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return nil, false
	}
	var (
		value interface{}
		found bool
	)
	if cs, ok := store.(ContextStore); ok {
		value, found = cs.GetCtx(ctx, key)
	} else {
		value, found = store.Get(key)
	}
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

// PutWithContext stores a value in the default cache store with context.
// Threads ctx through when the underlying store implements ContextStore.
func (m *Manager) PutWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return err
	}
	if cs, ok := store.(ContextStore); ok {
		if err := cs.PutCtx(ctx, key, value, ttl); err != nil {
			return err
		}
	} else {
		if err := store.Put(key, value, ttl); err != nil {
			return err
		}
	}
	m.dispatchCacheWritten(ctx, key, m.defaultStore, ttl)
	return nil
}

// Add atomically stores a value only if the key does not already exist.
// Returns (true, nil) on insert, (false, nil) on contention. See Cache.Add.
func (m *Manager) Add(key string, value interface{}, ttl time.Duration) (bool, error) {
	return m.AddWithContext(context.Background(), key, value, ttl)
}

// AddWithContext is the ctx-aware variant of Add. Threads ctx through
// when the store implements ContextStore so the underlying SETNX can be
// cancelled in-flight.
func (m *Manager) AddWithContext(ctx context.Context, key string, value interface{}, ttl time.Duration) (bool, error) {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return false, err
	}
	var (
		inserted bool
		addErr   error
	)
	if cs, ok := store.(ContextStore); ok {
		inserted, addErr = cs.AddCtx(ctx, key, value, ttl)
	} else {
		inserted, addErr = store.Add(key, value, ttl)
	}
	if addErr != nil {
		return false, addErr
	}
	if inserted {
		m.dispatchCacheWritten(ctx, key, m.defaultStore, ttl)
	}
	return inserted, nil
}

// Forever stores a value in the default cache store indefinitely
func (m *Manager) Forever(key string, value interface{}) error {
	return m.ForeverWithContext(context.Background(), key, value)
}

// ForeverWithContext stores a value in the default cache store indefinitely with context.
// Threads ctx through when the underlying store implements ContextStore.
func (m *Manager) ForeverWithContext(ctx context.Context, key string, value interface{}) error {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return err
	}
	if cs, ok := store.(ContextStore); ok {
		if err := cs.ForeverCtx(ctx, key, value); err != nil {
			return err
		}
	} else {
		if err := store.Forever(key, value); err != nil {
			return err
		}
	}
	m.dispatchCacheWritten(ctx, key, m.defaultStore, 0) // TTL=0 means forever
	return nil
}

// Forget removes a value from the default cache store
func (m *Manager) Forget(key string) error {
	return m.ForgetWithContext(context.Background(), key)
}

// ForgetWithContext removes a value from the default cache store with context.
// Threads ctx through when the underlying store implements ContextStore.
func (m *Manager) ForgetWithContext(ctx context.Context, key string) error {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return err
	}
	if cs, ok := store.(ContextStore); ok {
		if err := cs.ForgetCtx(ctx, key); err != nil {
			return err
		}
	} else {
		if err := store.Forget(key); err != nil {
			return err
		}
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

// Remember gets from default cache or computes and stores. The callback has
// no error return; on upstream failures the caller must use RememberE
// instead so the framework can skip the Put rather than poison the cache
// slot with a nil/zero value.
func (m *Manager) Remember(key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return m.RememberWithContext(context.Background(), key, ttl, callback)
}

// RememberWithContext gets from default cache or computes and stores, threading
// ctx through to the underlying store when it implements ContextStore. Cache
// reads and writes become cancellable when the request context is cancelled.
func (m *Manager) RememberWithContext(ctx context.Context, key string, ttl time.Duration, callback func() interface{}) (interface{}, error) {
	return m.RememberEWithContext(ctx, key, ttl, func() (interface{}, error) {
		return callback(), nil
	})
}

// RememberE is the error-aware variant of Remember. When the callback returns
// a non-nil error, the value is NOT written to the cache and the error is
// returned to the caller. This prevents transient upstream failures from
// pinning a nil/zero value for the full TTL.
func (m *Manager) RememberE(key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error) {
	return m.RememberEWithContext(context.Background(), key, ttl, callback)
}

// rememberLoserPollAttempts caps how long a Remember caller that lost the
// populate race spins waiting for the winner to write the real value.
// With rememberLoserPollInterval at 5ms this gives ~250ms of polling --
// enough for typical callback work (DB query, upstream API) to land,
// short enough that pathological slow callbacks fall through to local
// execution rather than blocking the request indefinitely. Losers
// that time out run the callback themselves, so correctness is
// preserved at the cost of one extra callback per slow-path key.
const (
	rememberLoserPollAttempts = 50
	rememberLoserPollInterval = 5 * time.Millisecond
)

// rememberLockSentinel is the placeholder value Add'd to the cache slot
// while the populate callback runs. It exists only inside the Manager's
// Remember path; callers never observe it because the populater
// overwrites the slot with the real value and the loser-poll loop
// treats the sentinel as "still populating, keep polling".
//
// The sentinel is a string with a NUL-prefixed magic marker so it
// survives JSON round-trip through the Redis driver (which serialises
// values as JSON) while remaining distinguishable from any legitimate
// user-supplied string value -- user strings cannot contain a leading
// NUL byte that round-trips through JSON unchanged because Go's
// json.Marshal escapes such bytes.
const rememberLockSentinel = "\x00\x00velocity/cache:remember-populating\x00\x00"

// isRememberSentinel reports whether a value read back from the store
// represents an in-flight populater marker. It accepts both the raw
// string form (memory driver, which stores values by reference) and the
// JSON-decoded form (redis/file drivers, which round-trip values
// through json.Marshal/Unmarshal).
func isRememberSentinel(v interface{}) bool {
	if s, ok := v.(string); ok {
		return s == rememberLockSentinel
	}
	return false
}

// rememberLockTTL is how long the placeholder lives if the populater
// crashes or is killed mid-callback. Once it elapses, the next caller
// can re-elect itself as the populater. Kept short so a misbehaving
// callback does not pin a slot for the full Remember TTL.
const rememberLockTTL = 30 * time.Second

// RememberEWithContext is the ctx + error-aware variant of Remember. Threads
// ctx through to the underlying ContextStore when available, and skips the
// cache Put when the callback returns an error.
//
// To mitigate the thundering-herd problem on cache misses, Remember uses
// the Store's atomic Add (SETNX) primitive to elect a single populater:
//   - Concurrent callers Get-miss, then race to Add a short-lived sentinel
//     placeholder for the slot.
//   - Exactly one caller wins the Add and runs the populate callback. On
//     success it Put's the real value (overwriting the sentinel); on
//     callback error it Forget's the placeholder so the next caller
//     re-elects.
//   - Losers observe Add returning false and poll Get with a small
//     backoff (capped at rememberLoserPollAttempts*rememberLoserPollInterval).
//     When they see the real value they return it; if they time out
//     they fall through to running the callback themselves -- correctness
//     is preserved at the cost of one extra callback in the pathological
//     slow-populater case.
//
// This converts the unbounded concurrent-callback problem into a single
// callback (typical case) or one fallback callback per slow populater,
// without requiring a separate in-process singleflight layer (which
// would not coordinate across multiple replicas).
func (m *Manager) RememberEWithContext(ctx context.Context, key string, ttl time.Duration, callback func() (interface{}, error)) (interface{}, error) {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return nil, err
	}
	cs, hasCtx := store.(ContextStore)

	getFn := func() (interface{}, bool) {
		if hasCtx {
			return cs.GetCtx(ctx, key)
		}
		return store.Get(key)
	}
	addFn := func(v interface{}, addTTL time.Duration) (bool, error) {
		if hasCtx {
			return cs.AddCtx(ctx, key, v, addTTL)
		}
		return store.Add(key, v, addTTL)
	}
	putFn := func(v interface{}) error {
		if hasCtx {
			return cs.PutCtx(ctx, key, v, ttl)
		}
		return store.Put(key, v, ttl)
	}
	forgetFn := func() error {
		if hasCtx {
			return cs.ForgetCtx(ctx, key)
		}
		return store.Forget(key)
	}

	if val, found := getFn(); found {
		if !isRememberSentinel(val) {
			m.dispatchCacheHit(ctx, key, m.defaultStore)
			return val, nil
		}
	}
	m.dispatchCacheMiss(ctx, key, m.defaultStore)

	// Pick the placeholder TTL: never longer than the caller's intended
	// TTL (so a misbehaving short-TTL key is not pinned for 30s by the
	// sentinel) and never longer than rememberLockTTL.
	lockTTL := rememberLockTTL
	if ttl > 0 && ttl < lockTTL {
		lockTTL = ttl
	}

	won, addErr := addFn(rememberLockSentinel, lockTTL)
	if addErr != nil {
		return nil, addErr
	}
	if won {
		// We are the populater. Run the callback and write the real
		// value over the sentinel. On callback error, drop the
		// sentinel so the next caller can re-elect.
		value, cbErr := callback()
		if cbErr != nil {
			_ = forgetFn()
			return nil, cbErr
		}
		if err := putFn(value); err != nil {
			_ = forgetFn()
			return nil, err
		}
		m.dispatchCacheWritten(ctx, key, m.defaultStore, ttl)
		return value, nil
	}

	// Lost the race -- another caller is populating. Poll briefly for
	// the real value before falling back to running the callback
	// ourselves. Honour ctx cancellation between polls.
	for attempt := 0; attempt < rememberLoserPollAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if val, found := getFn(); found {
			if !isRememberSentinel(val) {
				m.dispatchCacheHit(ctx, key, m.defaultStore)
				return val, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(rememberLoserPollInterval):
		}
	}
	// Timed out waiting for the winner. Fall through and run the
	// callback ourselves; better one extra callback than blocking the
	// request indefinitely.
	value, cbErr := callback()
	if cbErr != nil {
		return nil, cbErr
	}
	return value, nil
}

// RememberForever gets from default cache or computes and stores forever. See
// Remember for the error-handling caveat; use RememberForeverE for the
// error-aware variant.
func (m *Manager) RememberForever(key string, callback func() interface{}) (interface{}, error) {
	return m.RememberForeverWithContext(context.Background(), key, callback)
}

// RememberForeverWithContext gets from default cache or computes and stores
// forever, threading ctx through to the underlying ContextStore.
func (m *Manager) RememberForeverWithContext(ctx context.Context, key string, callback func() interface{}) (interface{}, error) {
	return m.RememberForeverEWithContext(ctx, key, func() (interface{}, error) {
		return callback(), nil
	})
}

// RememberForeverE is the error-aware variant of RememberForever. When the
// callback returns a non-nil error the value is NOT written to the cache and
// the error is returned to the caller.
func (m *Manager) RememberForeverE(key string, callback func() (interface{}, error)) (interface{}, error) {
	return m.RememberForeverEWithContext(context.Background(), key, callback)
}

// RememberForeverEWithContext is the ctx + error-aware variant of
// RememberForever. Uses the same single-flight populater election as
// RememberEWithContext (see that method's doc comment for the protocol).
// The placeholder sentinel is written with a short TTL even though the
// final value is stored Forever, so a crashed populater does not pin
// the slot permanently.
func (m *Manager) RememberForeverEWithContext(ctx context.Context, key string, callback func() (interface{}, error)) (interface{}, error) {
	store, err := m.DefaultStoreWithContext(ctx)
	if err != nil {
		return nil, err
	}
	cs, hasCtx := store.(ContextStore)

	getFn := func() (interface{}, bool) {
		if hasCtx {
			return cs.GetCtx(ctx, key)
		}
		return store.Get(key)
	}
	addFn := func(v interface{}, addTTL time.Duration) (bool, error) {
		if hasCtx {
			return cs.AddCtx(ctx, key, v, addTTL)
		}
		return store.Add(key, v, addTTL)
	}
	foreverFn := func(v interface{}) error {
		if hasCtx {
			return cs.ForeverCtx(ctx, key, v)
		}
		return store.Forever(key, v)
	}
	forgetFn := func() error {
		if hasCtx {
			return cs.ForgetCtx(ctx, key)
		}
		return store.Forget(key)
	}

	if val, found := getFn(); found {
		if !isRememberSentinel(val) {
			m.dispatchCacheHit(ctx, key, m.defaultStore)
			return val, nil
		}
	}
	m.dispatchCacheMiss(ctx, key, m.defaultStore)

	won, addErr := addFn(rememberLockSentinel, rememberLockTTL)
	if addErr != nil {
		return nil, addErr
	}
	if won {
		value, cbErr := callback()
		if cbErr != nil {
			_ = forgetFn()
			return nil, cbErr
		}
		if err := foreverFn(value); err != nil {
			_ = forgetFn()
			return nil, err
		}
		m.dispatchCacheWritten(ctx, key, m.defaultStore, 0)
		return value, nil
	}

	for attempt := 0; attempt < rememberLoserPollAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if val, found := getFn(); found {
			if !isRememberSentinel(val) {
				m.dispatchCacheHit(ctx, key, m.defaultStore)
				return val, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(rememberLoserPollInterval):
		}
	}
	value, cbErr := callback()
	if cbErr != nil {
		return nil, cbErr
	}
	return value, nil
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
