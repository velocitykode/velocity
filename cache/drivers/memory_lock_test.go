package drivers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryLock(t *testing.T) {
	store := NewMemoryStore("test_lock")
	defer store.Close()

	t.Run("GetAndRelease", func(t *testing.T) {
		lock := store.Lock("get-release")
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to return true on first call")
		}
		if !lock.Release() {
			t.Fatal("expected Release() to return true for owner")
		}
	})

	t.Run("DoubleGet", func(t *testing.T) {
		lock := store.Lock("double-get")
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected first Get() to succeed")
		}
		if lock.Get() {
			t.Fatal("expected second Get() to return false (already held)")
		}
	})

	t.Run("ReleaseWithoutGet", func(t *testing.T) {
		lock := store.Lock("release-no-get")

		if lock.Release() {
			t.Fatal("expected Release() to return false when lock was never acquired")
		}
	})

	t.Run("ReleaseWrongOwner", func(t *testing.T) {
		lock1 := store.Lock("wrong-owner")
		defer lock1.ForceRelease()

		if !lock1.Get() {
			t.Fatal("expected lock1.Get() to succeed")
		}

		lock2 := store.Lock("wrong-owner")
		if lock2.Release() {
			t.Fatal("expected Release() to return false for non-owner")
		}
	})

	t.Run("Owner", func(t *testing.T) {
		lock := store.Lock("owner-test")

		owner := lock.Owner()
		if owner == "" {
			t.Fatal("expected Owner() to return a non-empty string")
		}

		lock2 := store.Lock("owner-test-2")
		if lock.Owner() == lock2.Owner() {
			t.Fatal("expected different locks to have different owners")
		}
	})

	t.Run("ForceRelease", func(t *testing.T) {
		lock1 := store.Lock("force-release")
		if !lock1.Get() {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("force-release")
		if lock2.Get() {
			t.Fatal("expected Get() to fail while lock is held")
		}

		if err := lock2.ForceRelease(); err != nil {
			t.Fatalf("expected ForceRelease() to succeed, got %v", err)
		}

		lock3 := store.Lock("force-release")
		defer lock3.ForceRelease()
		if !lock3.Get() {
			t.Fatal("expected Get() to succeed after ForceRelease()")
		}
	})

	t.Run("ForceReleaseNonExistent", func(t *testing.T) {
		lock := store.Lock("force-release-nonexistent")
		if err := lock.ForceRelease(); err != nil {
			t.Fatalf("expected ForceRelease() on non-existent key to succeed, got %v", err)
		}
	})

	t.Run("RunSuccess", func(t *testing.T) {
		lock := store.Lock("run-success")

		called := false
		err := lock.Run(func() {
			called = true
		})
		if err != nil {
			t.Fatalf("expected Run() to return nil, got %v", err)
		}
		if !called {
			t.Fatal("expected callback to be called")
		}

		// Lock should be released after Run
		lock2 := store.Lock("run-success")
		defer lock2.ForceRelease()
		if !lock2.Get() {
			t.Fatal("expected lock to be released after Run()")
		}
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		lock1 := store.Lock("run-not-acquired")
		defer lock1.ForceRelease()

		if !lock1.Get() {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("run-not-acquired")
		err := lock2.Run(func() {
			t.Fatal("callback should not be called")
		})
		if err != ErrLockNotAcquired {
			t.Fatalf("expected ErrLockNotAcquired, got %v", err)
		}
	})

	t.Run("RunPanicRecovery", func(t *testing.T) {
		lock := store.Lock("run-panic")

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

		// Lock should still be released after panic
		lock2 := store.Lock("run-panic")
		defer lock2.ForceRelease()
		if !lock2.Get() {
			t.Fatal("expected lock to be released after panic in Run()")
		}
	})

	t.Run("BlockSuccess", func(t *testing.T) {
		lock := store.Lock("block-success")

		called := false
		err := lock.Block(time.Second, func() {
			called = true
		})
		if err != nil {
			t.Fatalf("expected Block() to return nil, got %v", err)
		}
		if !called {
			t.Fatal("expected callback to be called")
		}
	})

	t.Run("BlockTimeout", func(t *testing.T) {
		lock1 := store.Lock("block-timeout")
		defer lock1.ForceRelease()

		if !lock1.Get() {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("block-timeout")
		start := time.Now()
		err := lock2.Block(250*time.Millisecond, func() {
			t.Fatal("callback should not be called on timeout")
		})
		elapsed := time.Since(start)

		if err != ErrLockTimeout {
			t.Fatalf("expected ErrLockTimeout, got %v", err)
		}
		if elapsed < 200*time.Millisecond {
			t.Fatalf("expected Block() to wait at least 200ms, waited %v", elapsed)
		}
	})

	t.Run("BlockWaitsAndAcquires", func(t *testing.T) {
		lock1 := store.Lock("block-wait")
		if !lock1.Get() {
			t.Fatal("expected Get() to succeed")
		}

		// Release after a short delay
		go func() {
			time.Sleep(200 * time.Millisecond)
			lock1.Release()
		}()

		lock2 := store.Lock("block-wait")
		defer lock2.ForceRelease()

		called := false
		err := lock2.Block(time.Second, func() {
			called = true
		})
		if err != nil {
			t.Fatalf("expected Block() to succeed after waiting, got %v", err)
		}
		if !called {
			t.Fatal("expected callback to be called after lock became available")
		}
	})

	t.Run("BlockPanicRecovery", func(t *testing.T) {
		lock := store.Lock("block-panic")

		func() {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic to be re-raised")
				}
				if r != "block panic" {
					t.Fatalf("expected panic value 'block panic', got %v", r)
				}
			}()
			lock.Block(time.Second, func() {
				panic("block panic")
			})
		}()

		// Lock should be released after panic
		lock2 := store.Lock("block-panic")
		defer lock2.ForceRelease()
		if !lock2.Get() {
			t.Fatal("expected lock to be released after panic in Block()")
		}
	})

	t.Run("TTLExpiration", func(t *testing.T) {
		lock := store.Lock("ttl-expire", 200*time.Millisecond)
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to succeed")
		}

		// Should fail before expiry
		lock2 := store.Lock("ttl-expire")
		if lock2.Get() {
			lock2.ForceRelease()
			t.Fatal("expected Get() to fail before TTL expires")
		}

		// Wait for TTL to expire
		time.Sleep(300 * time.Millisecond)

		lock3 := store.Lock("ttl-expire")
		defer lock3.ForceRelease()
		if !lock3.Get() {
			t.Fatal("expected Get() to succeed after TTL expiration")
		}
	})

	t.Run("NoTTLDoesNotExpire", func(t *testing.T) {
		lock := store.Lock("no-ttl")
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to succeed")
		}

		time.Sleep(100 * time.Millisecond)

		lock2 := store.Lock("no-ttl")
		if lock2.Get() {
			lock2.ForceRelease()
			t.Fatal("expected lock without TTL to remain held")
		}
	})

	t.Run("DifferentKeysDontInterfere", func(t *testing.T) {
		lockA := store.Lock("key-a")
		defer lockA.ForceRelease()
		lockB := store.Lock("key-b")
		defer lockB.ForceRelease()

		if !lockA.Get() {
			t.Fatal("expected lockA.Get() to succeed")
		}
		if !lockB.Get() {
			t.Fatal("expected lockB.Get() to succeed (different key)")
		}
		if !lockA.Release() {
			t.Fatal("expected lockA.Release() to succeed")
		}
		if !lockB.Release() {
			t.Fatal("expected lockB.Release() to succeed")
		}
	})

	t.Run("RestoreLockCorrectOwner", func(t *testing.T) {
		lock := store.Lock("restore-correct")
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to succeed")
		}

		owner := lock.Owner()
		restored := store.RestoreLock("restore-correct", owner)

		if !restored.Release() {
			t.Fatal("expected restored lock with correct owner to release successfully")
		}
	})

	t.Run("RestoreLockWrongOwner", func(t *testing.T) {
		lock := store.Lock("restore-wrong")
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to succeed")
		}

		restored := store.RestoreLock("restore-wrong", "wrong-owner-token")
		if restored.Release() {
			t.Fatal("expected restored lock with wrong owner to fail release")
		}
	})

	t.Run("ConcurrentAccess", func(t *testing.T) {
		const goroutines = 20
		var acquired int32
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				lock := store.Lock("concurrent")
				if lock.Get() {
					atomic.AddInt32(&acquired, 1)
					time.Sleep(10 * time.Millisecond)
					lock.Release()
				}
			}()
		}

		wg.Wait()

		if acquired == 0 {
			t.Fatal("expected at least one goroutine to acquire the lock")
		}
		if acquired == int32(goroutines) {
			t.Fatal("expected mutual exclusion — not all goroutines should acquire simultaneously")
		}
	})

	t.Run("RunReleasesBeforeReturn", func(t *testing.T) {
		lock := store.Lock("run-releases")

		err := lock.Run(func() {})
		if err != nil {
			t.Fatalf("expected Run() to succeed, got %v", err)
		}

		// Immediately try to acquire with a new lock — should succeed
		lock2 := store.Lock("run-releases")
		defer lock2.ForceRelease()
		if !lock2.Get() {
			t.Fatal("expected lock to be available after Run() returns")
		}
	})

	t.Run("LockWithTTLViaStore", func(t *testing.T) {
		lock := store.Lock("store-ttl", 5*time.Second)
		defer lock.ForceRelease()

		if !lock.Get() {
			t.Fatal("expected Get() to succeed with TTL")
		}

		// Verify the lock is held
		lock2 := store.Lock("store-ttl")
		if lock2.Get() {
			lock2.ForceRelease()
			t.Fatal("expected lock to be held")
		}
	})

	t.Run("InstanceIsolation", func(t *testing.T) {
		store2 := NewMemoryStore("test_lock_2")
		defer store2.Close()

		lock1 := store.Lock("isolated-key")
		defer lock1.ForceRelease()

		if !lock1.Get() {
			t.Fatal("expected store1 lock to succeed")
		}

		// Different store instance should have its own lock store
		lock2 := store2.Lock("isolated-key")
		defer lock2.ForceRelease()
		if !lock2.Get() {
			t.Fatal("expected store2 lock to succeed (separate lock store)")
		}
	})
}
