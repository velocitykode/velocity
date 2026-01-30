// Package testing provides test helpers for the cache package.
//
// Usage:
//
//	func TestMyFeature(t *testing.T) {
//	    // Create a fake Redis store for testing
//	    store, cleanup := testing.FakeRedis(t, "myapp")
//	    defer cleanup()
//
//	    // Use the store in your tests
//	    store.Put("key", "value", time.Hour)
//	    // ...
//	}
package testing

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/cache/drivers"
)

// FakeRedis creates a fake Redis store backed by miniredis for testing.
// It returns the store and a cleanup function that should be called with defer.
//
// Example:
//
//	func TestCache(t *testing.T) {
//	    store, cleanup := testing.FakeRedis(t, "app")
//	    defer cleanup()
//
//	    store.Put("user:1", map[string]string{"name": "John"}, time.Hour)
//	    value, found := store.Get("user:1")
//	    // ...
//	}
func FakeRedis(t testing.TB, prefix string) (*drivers.RedisStore, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	store, err := drivers.NewRedisStore(prefix, mr.Host(), mr.Server().Addr().Port, "", 0)
	if err != nil {
		mr.Close()
		t.Fatalf("failed to create redis store: %v", err)
	}

	cleanup := func() {
		store.Close()
		mr.Close()
	}

	return store, cleanup
}

// FakeRedisWithServer creates a fake Redis store and also returns the miniredis
// server for advanced testing scenarios (e.g., simulating time, network errors).
//
// Example:
//
//	func TestCacheExpiration(t *testing.T) {
//	    store, server, cleanup := testing.FakeRedisWithServer(t, "app")
//	    defer cleanup()
//
//	    store.Put("key", "value", 100*time.Millisecond)
//	    server.FastForward(200 * time.Millisecond)
//
//	    _, found := store.Get("key")
//	    if found {
//	        t.Error("expected key to be expired")
//	    }
//	}
func FakeRedisWithServer(t testing.TB, prefix string) (*drivers.RedisStore, *miniredis.Miniredis, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	store, err := drivers.NewRedisStore(prefix, mr.Host(), mr.Server().Addr().Port, "", 0)
	if err != nil {
		mr.Close()
		t.Fatalf("failed to create redis store: %v", err)
	}

	cleanup := func() {
		store.Close()
		mr.Close()
	}

	return store, mr, cleanup
}

// FakeMemory creates an in-memory cache store for testing.
// It returns the store and a cleanup function that should be called with defer.
//
// Example:
//
//	func TestCache(t *testing.T) {
//	    store, cleanup := testing.FakeMemory(t, "app")
//	    defer cleanup()
//
//	    store.Put("key", "value", time.Hour)
//	    // ...
//	}
func FakeMemory(t testing.TB, prefix string) (*drivers.MemoryStore, func()) {
	t.Helper()

	store := drivers.NewMemoryStore(prefix)

	cleanup := func() {
		store.Close()
	}

	return store, cleanup
}

// FakeManager creates a cache manager configured with a fake Redis store.
// Useful for testing code that uses the cache.Manager interface.
//
// Example:
//
//	func TestWithManager(t *testing.T) {
//	    manager, cleanup := testing.FakeManager(t, "app")
//	    defer cleanup()
//
//	    manager.Put("key", "value", time.Hour)
//	    // ...
//	}
func FakeManager(t testing.TB, prefix string) (*cache.Manager, func()) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	config := &cache.Config{
		Default: "redis",
		Prefix:  prefix,
		Stores: map[string]cache.StoreConfig{
			"redis": {
				Driver:   cache.DriverRedis,
				Host:     mr.Host(),
				Port:     mr.Server().Addr().Port,
				Password: "",
				Database: 0,
			},
			"memory": {
				Driver: cache.DriverMemory,
			},
		},
	}

	manager := cache.NewManager(config)

	cleanup := func() {
		manager.Close()
		mr.Close()
	}

	return manager, cleanup
}

// FakeManagerMemory creates a cache manager configured with an in-memory store.
// Faster than FakeManager since it doesn't need miniredis.
//
// Example:
//
//	func TestWithMemoryManager(t *testing.T) {
//	    manager, cleanup := testing.FakeManagerMemory(t, "app")
//	    defer cleanup()
//
//	    manager.Put("key", "value", time.Hour)
//	    // ...
//	}
func FakeManagerMemory(t testing.TB, prefix string) (*cache.Manager, func()) {
	t.Helper()

	config := &cache.Config{
		Default: "memory",
		Prefix:  prefix,
		Stores: map[string]cache.StoreConfig{
			"memory": {
				Driver: cache.DriverMemory,
			},
		},
	}

	manager := cache.NewManager(config)

	cleanup := func() {
		manager.Close()
	}

	return manager, cleanup
}
