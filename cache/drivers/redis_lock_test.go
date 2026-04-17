package drivers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func setupRedisForLock(t *testing.T) (*RedisStore, *miniredis.Miniredis, func()) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}
	store, err := NewRedisStore("test_lock", mr.Host(), mr.Server().Addr().Port, "", 0, false)
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

func TestRedisLock(t *testing.T) {
	t.Run("Get", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		acquired := lock.Get()
		if !acquired {
			t.Error("Get() = false, want true")
		}
	})

	t.Run("DoubleGet", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock1 := store.Lock("resource1")
		lock2 := store.Lock("resource1")

		if !lock1.Get() {
			t.Fatal("first Get() = false, want true")
		}
		if lock2.Get() {
			t.Error("second Get() = true, want false (lock already held)")
		}
	})

	t.Run("Release", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		if !lock.Get() {
			t.Fatal("Get() = false, want true")
		}
		if !lock.Release() {
			t.Error("Release() = false, want true")
		}

		// Can reacquire after release
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("Get() after Release() = false, want true")
		}
	})

	t.Run("ReleaseWrongOwner", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		if !lock.Get() {
			t.Fatal("Get() = false, want true")
		}

		// Restore with a wrong owner
		wrongOwner := store.RestoreLock("resource1", "wrong-owner-token")
		if wrongOwner.Release() {
			t.Error("Release() with wrong owner = true, want false")
		}
	})

	t.Run("Run", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		called := false
		err := lock.Run(func() {
			called = true
		})
		if err != nil {
			t.Errorf("Run() error = %v, want nil", err)
		}
		if !called {
			t.Error("callback was not called")
		}

		// Lock should be released after Run
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("lock not released after Run()")
		}
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		holder := store.Lock("resource1")
		if !holder.Get() {
			t.Fatal("Get() = false, want true")
		}

		lock := store.Lock("resource1")
		err := lock.Run(func() {
			t.Error("callback should not be called when lock not acquired")
		})
		if err != ErrLockNotAcquired {
			t.Errorf("Run() error = %v, want ErrLockNotAcquired", err)
		}
	})

	t.Run("Block", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		holder := store.Lock("resource1")
		if !holder.Get() {
			t.Fatal("Get() = false, want true")
		}

		// Release the lock from another goroutine after a short delay
		go func() {
			time.Sleep(150 * time.Millisecond)
			holder.Release()
		}()

		lock := store.Lock("resource1")
		called := false
		err := lock.Block(2*time.Second, func() {
			called = true
		})
		if err != nil {
			t.Errorf("Block() error = %v, want nil", err)
		}
		if !called {
			t.Error("callback was not called")
		}
	})

	t.Run("BlockTimeout", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		holder := store.Lock("resource1")
		if !holder.Get() {
			t.Fatal("Get() = false, want true")
		}

		lock := store.Lock("resource1")
		err := lock.Block(250*time.Millisecond, func() {
			t.Error("callback should not be called on timeout")
		})
		if err != ErrLockTimeout {
			t.Errorf("Block() error = %v, want ErrLockTimeout", err)
		}
	})

	t.Run("TTLExpiration", func(t *testing.T) {
		store, mr, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", 2*time.Second)
		if !lock.Get() {
			t.Fatal("Get() = false, want true")
		}

		// Lock should still be held before TTL expires
		lock2 := store.Lock("resource1")
		if lock2.Get() {
			t.Error("Get() before TTL = true, want false")
		}

		// Fast-forward past the TTL
		mr.FastForward(3 * time.Second)

		// Lock should have expired, a new lock can be acquired
		lock3 := store.Lock("resource1")
		if !lock3.Get() {
			t.Error("Get() after TTL expiration = false, want true")
		}
	})

	t.Run("Owner", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		owner := lock.Owner()
		if owner == "" {
			t.Error("Owner() returned empty string, want non-empty UUID")
		}

		// Owner should remain consistent
		if lock.Owner() != owner {
			t.Error("Owner() returned different value on second call")
		}
	})

	t.Run("ForceRelease", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		if !lock.Get() {
			t.Fatal("Get() = false, want true")
		}

		// Force release from a different lock instance (different owner)
		other := store.Lock("resource1")
		err := other.ForceRelease()
		if err != nil {
			t.Errorf("ForceRelease() error = %v, want nil", err)
		}

		// Lock should be released; a new one can be acquired
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("Get() after ForceRelease() = false, want true")
		}
	})

	t.Run("RestoreLock", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")
		if !lock.Get() {
			t.Fatal("Get() = false, want true")
		}
		owner := lock.Owner()

		// Restore with the correct owner
		restored := store.RestoreLock("resource1", owner)
		if !restored.Release() {
			t.Error("restored lock Release() = false, want true")
		}

		// Lock should be released now
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("Get() after restored Release() = false, want true")
		}
	})

	t.Run("PanicRecovery", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")

		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic to be re-raised")
				}
				if r != "test panic" {
					t.Fatalf("expected panic value 'test panic', got %v", r)
				}
			}()
			lock.Run(func() {
				panic("test panic")
			})
		}()

		// Lock should be released despite the panic
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("Get() after panic in Run() = false, want true (lock should be released)")
		}
	})

	t.Run("PanicRecoveryBlock", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1")

		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic to be re-raised")
				}
				if r != "test panic in block" {
					t.Fatalf("expected panic value 'test panic in block', got %v", r)
				}
			}()
			lock.Block(time.Second, func() {
				panic("test panic in block")
			})
		}()

		// Lock should be released despite the panic
		lock2 := store.Lock("resource1")
		if !lock2.Get() {
			t.Error("Get() after panic in Block() = false, want true (lock should be released)")
		}
	})

	t.Run("DifferentKeys", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock1 := store.Lock("resource1")
		lock2 := store.Lock("resource2")

		if !lock1.Get() {
			t.Error("Get() on resource1 = false, want true")
		}
		if !lock2.Get() {
			t.Error("Get() on resource2 = false, want true (different key should not interfere)")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		var counter int64
		var maxConcurrent int64
		var currentHolders int64
		var wg sync.WaitGroup

		goroutines := 10
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()

				lock := store.Lock("shared-resource")
				err := lock.Block(5*time.Second, func() {
					cur := atomic.AddInt64(&currentHolders, 1)

					// Track the max number of concurrent holders
					for {
						old := atomic.LoadInt64(&maxConcurrent)
						if cur <= old || atomic.CompareAndSwapInt64(&maxConcurrent, old, cur) {
							break
						}
					}

					atomic.AddInt64(&counter, 1)
					time.Sleep(10 * time.Millisecond) // Hold the lock briefly
					atomic.AddInt64(&currentHolders, -1)
				})
				if err != nil {
					t.Errorf("Block() error = %v, want nil", err)
				}
			}()
		}

		wg.Wait()

		if atomic.LoadInt64(&counter) != int64(goroutines) {
			t.Errorf("counter = %d, want %d (all goroutines should have run)", atomic.LoadInt64(&counter), goroutines)
		}
		if atomic.LoadInt64(&maxConcurrent) > 1 {
			t.Errorf("maxConcurrent = %d, want 1 (only one goroutine should hold the lock at a time)", atomic.LoadInt64(&maxConcurrent))
		}
	})
}
