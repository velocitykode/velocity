package cache_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/velocitykode/velocity/cache"
)

func newRedisTestManager(t *testing.T) (*cache.Manager, *miniredis.Miniredis) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	config := &cache.Config{
		Default: "redis",
		Prefix:  "test",
		Stores: map[string]cache.StoreConfig{
			"redis": {
				Driver: cache.DriverRedis,
				Host:   mr.Host(),
				Port:   mr.Server().Addr().Port,
			},
		},
	}
	return cache.NewManager(config), mr
}

func TestManagerLock_Memory(t *testing.T) {
	t.Parallel()
	m := newTestManager()
	defer m.Close()

	t.Run("GetAndRelease", func(t *testing.T) {
		t.Parallel()
		lock := m.Lock("mem-get-release")
		if lock == nil {
			t.Fatal("Lock returned nil for memory store")
		}
		if !lock.Get() {
			t.Fatal("failed to acquire lock")
		}
		if !lock.Release() {
			t.Fatal("failed to release lock")
		}
	})

	t.Run("Run", func(t *testing.T) {
		t.Parallel()
		lock := m.Lock("mem-run")

		called := false
		err := lock.Run(func() { called = true })
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if !called {
			t.Fatal("callback was not called")
		}

		// Lock should be released after Run
		lock2 := m.Lock("mem-run")
		if !lock2.Get() {
			t.Fatal("failed to re-acquire lock after Run")
		}
		lock2.Release()
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		t.Parallel()
		lock1 := m.Lock("mem-run-held")
		if !lock1.Get() {
			t.Fatal("failed to acquire lock")
		}
		defer lock1.Release()

		lock2 := m.Lock("mem-run-held")
		err := lock2.Run(func() { t.Fatal("callback should not be called") })
		if err != cache.ErrLockNotAcquired {
			t.Fatalf("expected ErrLockNotAcquired, got %v", err)
		}
	})

	t.Run("Block", func(t *testing.T) {
		t.Parallel()
		lock := m.Lock("mem-block")

		called := false
		err := lock.Block(time.Second, func() { called = true })
		if err != nil {
			t.Fatalf("Block failed: %v", err)
		}
		if !called {
			t.Fatal("callback was not called")
		}
	})

	t.Run("BlockTimeout", func(t *testing.T) {
		t.Parallel()
		lock1 := m.Lock("mem-block-timeout")
		if !lock1.Get() {
			t.Fatal("failed to acquire lock")
		}
		defer lock1.Release()

		lock2 := m.Lock("mem-block-timeout")
		err := lock2.Block(200*time.Millisecond, func() {
			t.Fatal("callback should not be called")
		})
		if err != cache.ErrLockTimeout {
			t.Fatalf("expected ErrLockTimeout, got %v", err)
		}
	})

	t.Run("RestoreLock", func(t *testing.T) {
		t.Parallel()
		lock := m.Lock("mem-restore")
		if !lock.Get() {
			t.Fatal("failed to acquire lock")
		}

		restored := m.RestoreLock("mem-restore", lock.Owner())
		if restored == nil {
			t.Fatal("RestoreLock returned nil")
		}
		if !restored.Release() {
			t.Fatal("failed to release restored lock")
		}

		// Should be available now
		lock2 := m.Lock("mem-restore")
		if !lock2.Get() {
			t.Fatal("lock should be available after restored release")
		}
		lock2.Release()
	})

	t.Run("ForceRelease", func(t *testing.T) {
		t.Parallel()
		lock := m.Lock("mem-force")
		if !lock.Get() {
			t.Fatal("failed to acquire lock")
		}

		if err := lock.ForceRelease(); err != nil {
			t.Fatalf("ForceRelease failed: %v", err)
		}

		lock2 := m.Lock("mem-force")
		if !lock2.Get() {
			t.Fatal("lock should be available after ForceRelease")
		}
		lock2.Release()
	})

	t.Run("TTL", func(t *testing.T) {
		lock := m.Lock("mem-ttl", 200*time.Millisecond)
		if !lock.Get() {
			t.Fatal("failed to acquire lock with TTL")
		}

		lock2 := m.Lock("mem-ttl")
		if lock2.Get() {
			lock2.Release()
			t.Fatal("should not acquire while TTL lock is held")
		}

		time.Sleep(250 * time.Millisecond)

		lock3 := m.Lock("mem-ttl")
		if !lock3.Get() {
			t.Fatal("lock should be available after TTL expired")
		}
		lock3.Release()
	})

	t.Run("Owner", func(t *testing.T) {
		t.Parallel()
		lock1 := m.Lock("mem-owner-1")
		lock2 := m.Lock("mem-owner-2")

		if lock1.Owner() == "" {
			t.Fatal("owner should not be empty")
		}
		if lock1.Owner() == lock2.Owner() {
			t.Fatal("different locks should have different owners")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		t.Parallel()
		const goroutines = 20
		var acquired int32
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				lock := m.Lock("mem-concurrent")
				if lock.Get() {
					atomic.AddInt32(&acquired, 1)
					time.Sleep(5 * time.Millisecond)
					lock.Release()
				}
			}()
		}

		wg.Wait()
		if acquired == 0 {
			t.Fatal("expected at least one goroutine to acquire")
		}
	})
}

func TestManagerLock_Redis(t *testing.T) {
	t.Parallel()
	m, mr := newRedisTestManager(t)
	defer mr.Close()
	defer m.Close()

	t.Run("GetAndRelease", func(t *testing.T) {
		lock := m.Lock("redis-get-release")
		if lock == nil {
			t.Fatal("Lock returned nil for redis store")
		}
		if !lock.Get() {
			t.Fatal("failed to acquire redis lock")
		}
		if !lock.Release() {
			t.Fatal("failed to release redis lock")
		}
	})

	t.Run("Run", func(t *testing.T) {
		lock := m.Lock("redis-run")

		called := false
		err := lock.Run(func() { called = true })
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
		if !called {
			t.Fatal("callback was not called")
		}

		lock2 := m.Lock("redis-run")
		if !lock2.Get() {
			t.Fatal("failed to re-acquire lock after Run")
		}
		lock2.Release()
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		lock1 := m.Lock("redis-run-held")
		if !lock1.Get() {
			t.Fatal("failed to acquire lock")
		}
		defer lock1.Release()

		lock2 := m.Lock("redis-run-held")
		err := lock2.Run(func() { t.Fatal("callback should not be called") })
		if err != cache.ErrLockNotAcquired {
			t.Fatalf("expected ErrLockNotAcquired, got %v", err)
		}
	})

	t.Run("Block", func(t *testing.T) {
		lock := m.Lock("redis-block")

		called := false
		err := lock.Block(time.Second, func() { called = true })
		if err != nil {
			t.Fatalf("Block failed: %v", err)
		}
		if !called {
			t.Fatal("callback was not called")
		}
	})

	t.Run("BlockTimeout", func(t *testing.T) {
		lock1 := m.Lock("redis-block-timeout")
		if !lock1.Get() {
			t.Fatal("failed to acquire lock")
		}
		defer lock1.Release()

		lock2 := m.Lock("redis-block-timeout")
		err := lock2.Block(200*time.Millisecond, func() {
			t.Fatal("callback should not be called")
		})
		if err != cache.ErrLockTimeout {
			t.Fatalf("expected ErrLockTimeout, got %v", err)
		}
	})

	t.Run("RestoreLock", func(t *testing.T) {
		lock := m.Lock("redis-restore")
		if !lock.Get() {
			t.Fatal("failed to acquire lock")
		}

		restored := m.RestoreLock("redis-restore", lock.Owner())
		if restored == nil {
			t.Fatal("RestoreLock returned nil")
		}
		if !restored.Release() {
			t.Fatal("failed to release restored lock")
		}

		lock2 := m.Lock("redis-restore")
		if !lock2.Get() {
			t.Fatal("lock should be available after restored release")
		}
		lock2.Release()
	})

	t.Run("ForceRelease", func(t *testing.T) {
		lock := m.Lock("redis-force")
		if !lock.Get() {
			t.Fatal("failed to acquire lock")
		}

		if err := lock.ForceRelease(); err != nil {
			t.Fatalf("ForceRelease failed: %v", err)
		}

		lock2 := m.Lock("redis-force")
		if !lock2.Get() {
			t.Fatal("lock should be available after ForceRelease")
		}
		lock2.Release()
	})

	t.Run("TTL", func(t *testing.T) {
		lock := m.Lock("redis-ttl", 2*time.Second)
		if !lock.Get() {
			t.Fatal("failed to acquire lock with TTL")
		}

		lock2 := m.Lock("redis-ttl")
		if lock2.Get() {
			lock2.Release()
			t.Fatal("should not acquire while TTL lock is held")
		}

		mr.FastForward(3 * time.Second)

		lock3 := m.Lock("redis-ttl")
		if !lock3.Get() {
			t.Fatal("lock should be available after TTL expired")
		}
		lock3.Release()
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		const goroutines = 10
		var acquired int32
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				lock := m.Lock("redis-concurrent")
				if lock.Get() {
					atomic.AddInt32(&acquired, 1)
					time.Sleep(5 * time.Millisecond)
					lock.Release()
				}
			}()
		}

		wg.Wait()
		if acquired == 0 {
			t.Fatal("expected at least one goroutine to acquire")
		}
	})
}

func TestManagerLock_UnsupportedDriver(t *testing.T) {
	t.Parallel()
	config := &cache.Config{
		Default: "file",
		Prefix:  "",
		Stores: map[string]cache.StoreConfig{
			"file": {Driver: cache.DriverFile, Path: t.TempDir()},
		},
	}
	m := cache.NewManager(config)
	defer m.Close()

	if m.Lock("key") != nil {
		t.Fatal("expected nil lock for file store")
	}
	if m.RestoreLock("key", "owner") != nil {
		t.Fatal("expected nil restored lock for file store")
	}
}

func TestManagerLock_InvalidDefaultStore(t *testing.T) {
	t.Parallel()
	config := &cache.Config{
		Default: "nonexistent",
		Prefix:  "",
		Stores:  map[string]cache.StoreConfig{},
	}
	m := cache.NewManager(config)
	defer m.Close()

	if m.Lock("key") != nil {
		t.Fatal("expected nil lock when default store errors")
	}
	if m.RestoreLock("key", "owner") != nil {
		t.Fatal("expected nil restored lock when default store errors")
	}
}
