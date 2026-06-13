package cache_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

func TestMemoryStore(t *testing.T) {
	store := drivers.NewMemoryStore("test")
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	t.Run("GetSet", func(t *testing.T) {
		// Test Put and Get
		err := store.Put("key1", "value1", 1*time.Hour)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		val, found := store.Get("key1")
		if !found {
			t.Fatal("Key not found")
		}
		if val != "value1" {
			t.Fatalf("Expected value1, got %v", val)
		}
	})

	t.Run("GetString", func(t *testing.T) {
		store.Put("str", "hello", 1*time.Hour)

		str, found := store.GetString("str")
		if !found {
			t.Fatal("String not found")
		}
		if str != "hello" {
			t.Fatalf("Expected hello, got %s", str)
		}

		// Test non-string value
		store.Put("num", 123, 1*time.Hour)
		_, found = store.GetString("num")
		if found {
			t.Fatal("Should not return non-string as string")
		}
	})

	t.Run("Forever", func(t *testing.T) {
		err := store.Forever("permanent", "forever")
		if err != nil {
			t.Fatalf("Forever failed: %v", err)
		}

		val, found := store.Get("permanent")
		if !found {
			t.Fatal("Permanent key not found")
		}
		if val != "forever" {
			t.Fatalf("Expected forever, got %v", val)
		}
	})

	t.Run("Forget", func(t *testing.T) {
		store.Put("temp", "value", 1*time.Hour)

		err := store.Forget("temp")
		if err != nil {
			t.Fatalf("Forget failed: %v", err)
		}

		_, found := store.Get("temp")
		if found {
			t.Fatal("Key should be forgotten")
		}
	})

	t.Run("TTL", func(t *testing.T) {
		// Test expiration
		err := store.Put("expire", "value", 100*time.Millisecond)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		// Should exist immediately
		if !store.Has("expire") {
			t.Fatal("Key should exist")
		}

		// Wait for expiration
		time.Sleep(150 * time.Millisecond)

		// Should not exist after expiration
		if store.Has("expire") {
			t.Fatal("Key should have expired")
		}
	})

	t.Run("Increment", func(t *testing.T) {
		// Start from zero
		val, err := store.Increment("counter", 1)
		if err != nil {
			t.Fatalf("Increment failed: %v", err)
		}
		if val != 1 {
			t.Fatalf("Expected 1, got %d", val)
		}

		// Increment again
		val, err = store.Increment("counter", 5)
		if err != nil {
			t.Fatalf("Increment failed: %v", err)
		}
		if val != 6 {
			t.Fatalf("Expected 6, got %d", val)
		}
	})

	t.Run("Decrement", func(t *testing.T) {
		store.Put("counter2", int64(10), 1*time.Hour)

		val, err := store.Decrement("counter2", 3)
		if err != nil {
			t.Fatalf("Decrement failed: %v", err)
		}
		if val != 7 {
			t.Fatalf("Expected 7, got %d", val)
		}
	})

	t.Run("Remember", func(t *testing.T) {
		callCount := 0
		callback := func() interface{} {
			callCount++
			return "computed"
		}

		// First call should execute callback
		val, err := store.Remember("remember", 1*time.Hour, callback)
		if err != nil {
			t.Fatalf("Remember failed: %v", err)
		}
		if val != "computed" {
			t.Fatalf("Expected computed, got %v", val)
		}
		if callCount != 1 {
			t.Fatalf("Expected callback to be called once, called %d times", callCount)
		}

		// Second call should use cache
		val, err = store.Remember("remember", 1*time.Hour, callback)
		if err != nil {
			t.Fatalf("Remember failed: %v", err)
		}
		if val != "computed" {
			t.Fatalf("Expected computed, got %v", val)
		}
		if callCount != 1 {
			t.Fatalf("Expected callback to be called once, called %d times", callCount)
		}
	})

	t.Run("RememberForever", func(t *testing.T) {
		callCount := 0
		callback := func() interface{} {
			callCount++
			return "permanent"
		}

		val, err := store.RememberForever("remember-forever", callback)
		if err != nil {
			t.Fatalf("RememberForever failed: %v", err)
		}
		if val != "permanent" {
			t.Fatalf("Expected permanent, got %v", val)
		}

		// Second call should use cache
		val2, err2 := store.RememberForever("remember-forever", callback)
		if err2 != nil {
			t.Fatalf("Second RememberForever failed: %v", err2)
		}
		if val2 != "permanent" {
			t.Fatalf("Expected cached permanent, got %v", val2)
		}
		if callCount != 1 {
			t.Fatalf("Callback should only be called once")
		}
	})

	t.Run("Many", func(t *testing.T) {
		store.Put("multi1", "value1", 1*time.Hour)
		store.Put("multi2", "value2", 1*time.Hour)
		store.Put("multi3", "value3", 1*time.Hour)

		values := store.Many([]string{"multi1", "multi2", "multi4"})

		if len(values) != 2 {
			t.Fatalf("Expected 2 values, got %d", len(values))
		}
		if values["multi1"] != "value1" {
			t.Fatalf("Expected value1 for multi1")
		}
		if values["multi2"] != "value2" {
			t.Fatalf("Expected value2 for multi2")
		}
		if _, exists := values["multi4"]; exists {
			t.Fatal("multi4 should not exist")
		}
	})

	t.Run("PutMany", func(t *testing.T) {
		items := map[string]interface{}{
			"batch1": "value1",
			"batch2": "value2",
			"batch3": "value3",
		}

		err := store.PutMany(items, 1*time.Hour)
		if err != nil {
			t.Fatalf("PutMany failed: %v", err)
		}

		for key, expectedValue := range items {
			val, found := store.Get(key)
			if !found {
				t.Fatalf("Key %s not found", key)
			}
			if val != expectedValue {
				t.Fatalf("Expected %v for %s, got %v", expectedValue, key, val)
			}
		}
	})

	t.Run("Has", func(t *testing.T) {
		store.Put("exists", "value", 1*time.Hour)

		if !store.Has("exists") {
			t.Fatal("Key should exist")
		}
		if store.Has("not-exists") {
			t.Fatal("Key should not exist")
		}
	})

	t.Run("Flush", func(t *testing.T) {
		store.Put("flush1", "value1", 1*time.Hour)
		store.Put("flush2", "value2", 1*time.Hour)

		err := store.Flush()
		if err != nil {
			t.Fatalf("Flush failed: %v", err)
		}

		if store.Has("flush1") || store.Has("flush2") {
			t.Fatal("All keys should be flushed")
		}
	})

	t.Run("Prefix", func(t *testing.T) {
		if store.GetPrefix() != "test" {
			t.Fatalf("Expected prefix 'test', got '%s'", store.GetPrefix())
		}
	})

	t.Run("Concurrent", func(t *testing.T) {
		var wg sync.WaitGroup
		iterations := 100

		// Concurrent writes
		for i := 0; i < iterations; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				key := fmt.Sprintf("concurrent%d", n)
				store.Put(key, n, 1*time.Hour)
			}(i)
		}

		// Concurrent reads
		for i := 0; i < iterations; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				key := fmt.Sprintf("concurrent%d", n)
				store.Get(key)
			}(i)
		}

		// Concurrent increments
		for i := 0; i < iterations; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				store.Increment("concurrent-counter", 1)
			}()
		}

		wg.Wait()

		// Verify counter
		val, _ := store.Get("concurrent-counter")
		if val != int64(iterations) {
			t.Fatalf("Expected counter to be %d, got %v", iterations, val)
		}
	})
}

func TestFileStore(t *testing.T) {
	store, err := drivers.NewFileStore("test", "testdata/cache")
	if err != nil {
		t.Fatalf("Failed to create file store: %v", err)
	}
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()
	defer os.RemoveAll("testdata")

	t.Run("BasicOperations", func(t *testing.T) {
		// Put and Get
		err := store.Put("file-key", "file-value", 1*time.Hour)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		val, found := store.Get("file-key")
		if !found {
			t.Fatal("Key not found")
		}
		if val != "file-value" {
			t.Fatalf("Expected file-value, got %v", val)
		}

		// Forever
		err = store.Forever("permanent-file", "forever")
		if err != nil {
			t.Fatalf("Forever failed: %v", err)
		}

		// Forget
		err = store.Forget("file-key")
		if err != nil {
			t.Fatalf("Forget failed: %v", err)
		}

		if store.Has("file-key") {
			t.Fatal("Key should be forgotten")
		}
	})

	t.Run("NumericOperations", func(t *testing.T) {
		val, err := store.Increment("file-counter", 5)
		if err != nil {
			t.Fatalf("Increment failed: %v", err)
		}
		if val != 5 {
			t.Fatalf("Expected 5, got %d", val)
		}

		val, err = store.Decrement("file-counter", 2)
		if err != nil {
			t.Fatalf("Decrement failed: %v", err)
		}
		if val != 3 {
			t.Fatalf("Expected 3, got %d", val)
		}
	})

	t.Run("ComplexTypes", func(t *testing.T) {
		// Store complex type
		data := map[string]interface{}{
			"name":  "John",
			"age":   30,
			"email": "john@example.com",
		}

		err := store.Put("user", data, 1*time.Hour)
		if err != nil {
			t.Fatalf("Put complex type failed: %v", err)
		}

		val, found := store.Get("user")
		if !found {
			t.Fatal("Complex type not found")
		}

		userMap, ok := val.(map[string]interface{})
		if !ok {
			t.Fatal("Failed to cast to map")
		}
		if userMap["name"] != "John" {
			t.Fatalf("Expected John, got %v", userMap["name"])
		}
	})
}

func TestManager(t *testing.T) {
	config := &cache.Config{
		Default: "memory",
		Prefix:  "test",
		Stores: map[string]cache.StoreConfig{
			"memory": {
				Driver: cache.DriverMemory,
			},
			"file": {
				Driver: cache.DriverFile,
				Path:   "testdata/manager",
			},
		},
	}

	manager := cache.NewManager(config)
	defer func() { _ = manager.Shutdown(context.Background()) }()
	defer os.RemoveAll("testdata")

	t.Run("DefaultStore", func(t *testing.T) {
		err := manager.Put("key", "value", 1*time.Hour)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		val, found := manager.Get("key")
		if !found {
			t.Fatal("Key not found in default store")
		}
		if val != "value" {
			t.Fatalf("Expected value, got %v", val)
		}
	})

	t.Run("NamedStores", func(t *testing.T) {
		memStore, err := manager.Store("memory")
		if err != nil {
			t.Fatalf("Failed to get memory store: %v", err)
		}

		fileStore, err := manager.Store("file")
		if err != nil {
			t.Fatalf("Failed to get file store: %v", err)
		}

		// Put in different stores
		memStore.Put("mem-key", "mem-value", 1*time.Hour)
		fileStore.Put("file-key", "file-value", 1*time.Hour)

		// Verify isolation
		if memStore.Has("file-key") {
			t.Fatal("Memory store should not have file-key")
		}
		if fileStore.Has("mem-key") {
			t.Fatal("File store should not have mem-key")
		}
	})

	t.Run("InvalidStore", func(t *testing.T) {
		_, err := manager.Store("invalid")
		if err == nil {
			t.Fatal("Should return error for invalid store")
		}
	})
}

// TestStoreConfigValidate_AcceptsThirdPartyDrivers pins the contract that
// StoreConfig.Validate does NOT reject driver names outside the built-in
// set. The previous allowlist meant a user who called
// cache.Drivers().Register("dragonfly", ...) hit a validation error before
// resolution, killing the registry's extensibility. Validate now only
// requires a non-empty driver name.
func TestStoreConfigValidate_AcceptsThirdPartyDrivers(t *testing.T) {
	cases := []struct {
		name    string
		cfg     cache.StoreConfig
		wantErr bool
	}{
		{"empty driver rejected", cache.StoreConfig{}, true},
		{"third-party driver accepted", cache.StoreConfig{Driver: "dragonfly"}, false},
		{"redis with no host accepted at validate (factory enforces)", cache.StoreConfig{Driver: cache.DriverRedis}, false},
		{"memory accepted", cache.StoreConfig{Driver: cache.DriverMemory}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestManager_ResolvesThirdPartyDriver verifies that a driver registered
// via Drivers().Register can be resolved end-to-end, including the
// per-store config validation step that previously rejected unknown
// driver names.
func TestManager_ResolvesThirdPartyDriver(t *testing.T) {
	const name = "test-third-party"
	prev := cache.Drivers().Override(name, func(_ context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		// Reuse the in-memory store so the test does not need a real backend.
		return drivers.NewMemoryStore(cfg.Prefix), nil
	})
	t.Cleanup(func() { cache.Drivers().Override(name, prev) })

	manager := cache.NewManager(&cache.Config{
		Default: "third",
		Stores: map[string]cache.StoreConfig{
			"third": {Driver: name},
		},
	})
	defer func() { _ = manager.Shutdown(context.Background()) }()

	if err := manager.Put("k", "v", time.Hour); err != nil {
		t.Fatalf("Put on third-party-driver-backed store: %v", err)
	}
	if v, ok := manager.Get("k"); !ok || v != "v" {
		t.Fatalf("Get returned (%v, %v); want (v, true)", v, ok)
	}
}

// TestManager_StoreWithContext_RespectsCallerDeadline verifies that the
// caller's ctx is threaded into the driver factory the first time a
// store is materialised. We use a custom driver factory that observes
// the ctx to confirm it is NOT context.Background.
func TestManager_StoreWithContext_RespectsCallerDeadline(t *testing.T) {
	const name = "test-ctx-observer"
	type ctxKey struct{}
	wantKey := ctxKey{}

	var observed context.Context
	prev := cache.Drivers().Override(name, func(ctx context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		observed = ctx
		return drivers.NewMemoryStore(cfg.Prefix), nil
	})
	t.Cleanup(func() { cache.Drivers().Override(name, prev) })

	manager := cache.NewManager(&cache.Config{
		Default: "obs",
		Stores: map[string]cache.StoreConfig{
			"obs": {Driver: name},
		},
	})
	defer func() { _ = manager.Shutdown(context.Background()) }()

	ctx := context.WithValue(context.Background(), wantKey, "yes")
	if _, err := manager.StoreWithContext(ctx, "obs"); err != nil {
		t.Fatalf("StoreWithContext error = %v", err)
	}
	if observed == nil {
		t.Fatal("factory did not observe a ctx")
	}
	if got, _ := observed.Value(wantKey).(string); got != "yes" {
		t.Errorf("factory ctx value = %q; want caller ctx threaded through", got)
	}
}

func newTestManager() *cache.Manager {
	return cache.NewManager(&cache.Config{
		Default: "memory",
		Prefix:  "",
		Stores: map[string]cache.StoreConfig{
			"memory": {Driver: cache.DriverMemory},
		},
	})
}

func TestManagerConvenienceMethods(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	t.Run("PutGetHasForget", func(t *testing.T) {
		err := m.Put("global-key", "global-value", 1*time.Hour)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}

		val, found := m.Get("global-key")
		if !found {
			t.Fatal("Key not found")
		}
		if val != "global-value" {
			t.Fatalf("Expected global-value, got %v", val)
		}

		if !m.Has("global-key") {
			t.Fatal("Has failed")
		}

		m.Forget("global-key")
		if m.Has("global-key") {
			t.Fatal("Forget failed")
		}
	})

	t.Run("Remember", func(t *testing.T) {
		callCount := 0
		val, err := m.Remember("global-remember", 1*time.Hour, func() interface{} {
			callCount++
			return "remembered"
		})

		if err != nil {
			t.Fatalf("Remember failed: %v", err)
		}
		if val != "remembered" {
			t.Fatalf("Expected remembered, got %v", val)
		}

		// Second call should use cache
		m.Remember("global-remember", 1*time.Hour, func() interface{} {
			callCount++
			return "remembered"
		})

		if callCount != 1 {
			t.Fatal("Callback should only be called once")
		}
	})
}

func TestManagerGetString(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	m.Put("test_str", "hello", 1*time.Hour)

	str, found := m.GetString("test_str")
	if !found || str != "hello" {
		t.Error("GetString failed")
	}
}

func TestManagerForever(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	err := m.Forever("forever_key", "forever_value")
	if err != nil {
		t.Fatalf("Forever failed: %v", err)
	}

	val, found := m.Get("forever_key")
	if !found || val != "forever_value" {
		t.Error("Forever value not stored")
	}
}

func TestManagerFlush(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	m.Put("key1", "val1", 1*time.Hour)
	m.Put("key2", "val2", 1*time.Hour)

	err := m.Flush()
	if err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	_, found := m.Get("key1")
	if found {
		t.Error("Key should be flushed")
	}
}

func TestManagerIncrement(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	m.Put("counter", 10, 1*time.Hour)

	newVal, err := m.Increment("counter", 5)
	if err != nil {
		t.Fatalf("Increment failed: %v", err)
	}
	if newVal != 15 {
		t.Errorf("Expected 15, got %d", newVal)
	}
}

func TestManagerDecrement(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	m.Put("counter", 20, 1*time.Hour)

	newVal, err := m.Decrement("counter", 5)
	if err != nil {
		t.Fatalf("Decrement failed: %v", err)
	}
	if newVal != 15 {
		t.Errorf("Expected 15, got %d", newVal)
	}
}

func TestManagerRememberForever(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	val, err := m.RememberForever("remember_forever_key", func() interface{} {
		return "computed_value"
	})
	if err != nil {
		t.Fatalf("RememberForever failed: %v", err)
	}
	if val != "computed_value" {
		t.Error("RememberForever returned wrong value")
	}

	// Should return cached value
	val2, _ := m.Get("remember_forever_key")
	if val2 != "computed_value" {
		t.Error("RememberForever didn't cache value")
	}
}

func TestManagerMany(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	m.Put("key1", "val1", 1*time.Hour)
	m.Put("key2", "val2", 1*time.Hour)

	values := m.Many([]string{"key1", "key2"})
	if len(values) < 2 {
		t.Errorf("Expected at least 2 values, got %d", len(values))
	}
	if values["key1"] != "val1" {
		t.Error("key1 value incorrect")
	}
	if values["key2"] != "val2" {
		t.Error("key2 value incorrect")
	}
}

func TestManagerPutMany(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	values := map[string]interface{}{
		"k1": "v1",
		"k2": "v2",
	}
	err := m.PutMany(values, 1*time.Hour)
	if err != nil {
		t.Fatalf("PutMany failed: %v", err)
	}

	val, found := m.Get("k1")
	if !found || val != "v1" {
		t.Error("PutMany didn't store k1")
	}
}

func TestManagerGetStore(t *testing.T) {
	m := newTestManager()
	defer func() { _ = m.Shutdown(context.Background()) }()

	store, err := m.Store("memory")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if store == nil {
		t.Error("Store returned nil")
	}
}

// shutdownErrStore wraps *drivers.MemoryStore so it satisfies cache.Store
// (and contract.ShutdownAware) but returns a configurable error from
// Shutdown. The embedded MemoryStore.Shutdown is shadowed by the explicit
// method below.
type shutdownErrStore struct {
	*drivers.MemoryStore
	err    error
	called atomic.Bool
}

func (s *shutdownErrStore) Shutdown(ctx context.Context) error {
	s.called.Store(true)
	// Still let the underlying store release its goroutine so the test
	// process exits cleanly under -race.
	_ = s.MemoryStore.Shutdown(ctx)
	return s.err
}

// TestManager_Shutdown_ReturnsStoreErrors pins the contract that
// Manager.Shutdown surfaces per-store Shutdown failures via errors.Join
// instead of silently swallowing them, while still attempting Shutdown on
// every store and clearing the internal store map.
func TestManager_Shutdown_ReturnsStoreErrors(t *testing.T) {
	errBoom := errors.New("boom")
	errKaboom := errors.New("kaboom")

	const failName = "fail-driver"
	const failName2 = "fail-driver-2"
	const okName = "ok-driver"

	var failStore, failStore2, okStore *shutdownErrStore

	prevFail := cache.Drivers().Override(failName, func(_ context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		failStore = &shutdownErrStore{MemoryStore: drivers.NewMemoryStore(cfg.Prefix), err: errBoom}
		return failStore, nil
	})
	t.Cleanup(func() { cache.Drivers().Override(failName, prevFail) })

	prevFail2 := cache.Drivers().Override(failName2, func(_ context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		failStore2 = &shutdownErrStore{MemoryStore: drivers.NewMemoryStore(cfg.Prefix), err: errKaboom}
		return failStore2, nil
	})
	t.Cleanup(func() { cache.Drivers().Override(failName2, prevFail2) })

	prevOK := cache.Drivers().Override(okName, func(_ context.Context, cfg cache.StoreConfig) (cache.Store, error) {
		okStore = &shutdownErrStore{MemoryStore: drivers.NewMemoryStore(cfg.Prefix), err: nil}
		return okStore, nil
	})
	t.Cleanup(func() { cache.Drivers().Override(okName, prevOK) })

	m := cache.NewManager(&cache.Config{
		Default: "fail",
		Stores: map[string]cache.StoreConfig{
			"fail":  {Driver: failName},
			"fail2": {Driver: failName2},
			"ok":    {Driver: okName},
		},
	})

	// Materialise all three stores so they end up in the manager's map.
	if _, err := m.Store("fail"); err != nil {
		t.Fatalf("Store(fail) error = %v", err)
	}
	if _, err := m.Store("fail2"); err != nil {
		t.Fatalf("Store(fail2) error = %v", err)
	}
	if _, err := m.Store("ok"); err != nil {
		t.Fatalf("Store(ok) error = %v", err)
	}

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("Manager.Shutdown returned nil; want joined error from failing stores")
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("Manager.Shutdown error does not wrap errBoom: %v", err)
	}
	if !errors.Is(err, errKaboom) {
		t.Errorf("Manager.Shutdown error does not wrap errKaboom: %v", err)
	}

	// Every store must have had its Shutdown invoked even though the
	// first one (iteration-order-dependent) returned an error.
	if !failStore.called.Load() {
		t.Error("failStore.Shutdown was not called")
	}
	if !failStore2.called.Load() {
		t.Error("failStore2.Shutdown was not called")
	}
	if !okStore.called.Load() {
		t.Error("okStore.Shutdown was not called")
	}

	// A second Shutdown with no intervening Store() must be an idempotent
	// nil no-op: the map is already empty, so it returns nil and does not
	// re-invoke any of the original children.
	failStore.called.Store(false)
	failStore2.called.Store(false)
	okStore.called.Store(false)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("second Manager.Shutdown returned %v; want nil no-op", err)
	}
	if failStore.called.Load() || failStore2.called.Load() || okStore.called.Load() {
		t.Error("second Shutdown re-invoked original children; map was not cleared")
	}

	// Map must be cleared regardless of errors: a subsequent Store()
	// call must re-create the store via the factory, observable via a
	// fresh pointer.
	prevFailPtr := failStore
	if _, err := m.Store("fail"); err != nil {
		t.Fatalf("Store(fail) after Shutdown error = %v", err)
	}
	if failStore == prevFailPtr {
		t.Error("manager did not clear its store map; factory was not re-invoked")
	}

	// Tidy up the freshly-created store so the process exits cleanly.
	_ = m.Shutdown(context.Background())
}
