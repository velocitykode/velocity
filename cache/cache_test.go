package cache_test

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/cache/drivers"
)

func TestMemoryStore(t *testing.T) {
	store := drivers.NewMemoryStore("test")
	store.Start()
	defer store.Close()

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
	defer store.Close()
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
	defer manager.Close()
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
	defer m.Close()

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
	defer m.Close()

	m.Put("test_str", "hello", 1*time.Hour)

	str, found := m.GetString("test_str")
	if !found || str != "hello" {
		t.Error("GetString failed")
	}
}

func TestManagerForever(t *testing.T) {
	m := newTestManager()
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

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
	defer m.Close()

	store, err := m.Store("memory")
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if store == nil {
		t.Error("Store returned nil")
	}
}
