package testing

import (
	"testing"
	"time"
)

func TestFakeRedis(t *testing.T) {
	t.Run("creates functional redis store", func(t *testing.T) {
		store, cleanup := FakeRedis(t, "test")
		defer cleanup()

		// Test basic operations
		err := store.Put("key", "value", time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("key")
		if !found {
			t.Fatal("Get() key not found")
		}
		if got != "value" {
			t.Errorf("Get() = %v, want value", got)
		}
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		store, cleanup := FakeRedis(t, "myprefix")
		defer cleanup()

		if store.GetPrefix() != "myprefix" {
			t.Errorf("GetPrefix() = %v, want myprefix", store.GetPrefix())
		}
	})

	t.Run("cleanup closes connections", func(t *testing.T) {
		store, cleanup := FakeRedis(t, "test")
		cleanup()

		// After cleanup, operations should fail
		err := store.Put("key", "value", time.Hour)
		if err == nil {
			t.Error("expected error after cleanup")
		}
	})
}

func TestFakeRedisWithServer(t *testing.T) {
	t.Run("allows time manipulation", func(t *testing.T) {
		store, server, cleanup := FakeRedisWithServer(t, "test")
		defer cleanup()

		store.Put("key", "value", 100*time.Millisecond)

		// Key should exist initially
		if !store.Has("key") {
			t.Fatal("key should exist initially")
		}

		// Fast forward time
		server.FastForward(200 * time.Millisecond)

		// Key should be expired
		if store.Has("key") {
			t.Error("key should be expired after FastForward")
		}
	})
}

func TestFakeMemory(t *testing.T) {
	t.Run("creates functional memory store", func(t *testing.T) {
		store, cleanup := FakeMemory(t, "test")
		defer cleanup()

		err := store.Put("key", "value", time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := store.Get("key")
		if !found {
			t.Fatal("Get() key not found")
		}
		if got != "value" {
			t.Errorf("Get() = %v, want value", got)
		}
	})

	t.Run("uses correct prefix", func(t *testing.T) {
		store, cleanup := FakeMemory(t, "memprefix")
		defer cleanup()

		if store.GetPrefix() != "memprefix" {
			t.Errorf("GetPrefix() = %v, want memprefix", store.GetPrefix())
		}
	})
}

func TestFakeManager(t *testing.T) {
	t.Run("creates functional manager with redis", func(t *testing.T) {
		manager, cleanup := FakeManager(t, "test")
		defer cleanup()

		err := manager.Put("key", "value", time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := manager.Get("key")
		if !found {
			t.Fatal("Get() key not found")
		}
		if got != "value" {
			t.Errorf("Get() = %v, want value", got)
		}
	})

	t.Run("can access named stores", func(t *testing.T) {
		manager, cleanup := FakeManager(t, "test")
		defer cleanup()

		// Should be able to access redis store
		redisStore, err := manager.Store("redis")
		if err != nil {
			t.Fatalf("Store(redis) error = %v", err)
		}
		if redisStore == nil {
			t.Fatal("Store(redis) returned nil")
		}

		// Should be able to access memory store
		memStore, err := manager.Store("memory")
		if err != nil {
			t.Fatalf("Store(memory) error = %v", err)
		}
		if memStore == nil {
			t.Fatal("Store(memory) returned nil")
		}
	})
}

func TestFakeManagerMemory(t *testing.T) {
	t.Run("creates functional manager with memory", func(t *testing.T) {
		manager, cleanup := FakeManagerMemory(t, "test")
		defer cleanup()

		err := manager.Put("key", "value", time.Hour)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}

		got, found := manager.Get("key")
		if !found {
			t.Fatal("Get() key not found")
		}
		if got != "value" {
			t.Errorf("Get() = %v, want value", got)
		}
	})

	t.Run("uses memory as default", func(t *testing.T) {
		manager, cleanup := FakeManagerMemory(t, "test")
		defer cleanup()

		store, err := manager.DefaultStore()
		if err != nil {
			t.Fatalf("DefaultStore() error = %v", err)
		}

		// Store operations should work
		err = store.Put("direct", "value", time.Hour)
		if err != nil {
			t.Fatalf("store.Put() error = %v", err)
		}
	})
}

func TestFakeRedis_Isolation(t *testing.T) {
	t.Run("multiple stores are isolated", func(t *testing.T) {
		store1, cleanup1 := FakeRedis(t, "app1")
		defer cleanup1()

		store2, cleanup2 := FakeRedis(t, "app2")
		defer cleanup2()

		// Write to both stores
		store1.Put("key", "value1", time.Hour)
		store2.Put("key", "value2", time.Hour)

		// Each store should have its own value
		got1, _ := store1.Get("key")
		got2, _ := store2.Get("key")

		if got1 != "value1" {
			t.Errorf("store1 Get() = %v, want value1", got1)
		}
		if got2 != "value2" {
			t.Errorf("store2 Get() = %v, want value2", got2)
		}
	})
}
