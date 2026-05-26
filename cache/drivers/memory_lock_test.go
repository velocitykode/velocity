package drivers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryLock(t *testing.T) {
	store := NewMemoryStore("test_lock")
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	ctx := context.Background()

	t.Run("GetAndRelease", func(t *testing.T) {
		lock := store.Lock("get-release", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to return true on first call")
		}
		if !lock.Release(ctx) {
			t.Fatal("expected Release() to return true for owner")
		}
	})

	t.Run("DoubleGet", func(t *testing.T) {
		lock := store.Lock("double-get", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected first Get() to succeed")
		}
		if lock.Get(ctx) {
			t.Fatal("expected second Get() to return false (already held)")
		}
	})

	t.Run("ReleaseWithoutGet", func(t *testing.T) {
		lock := store.Lock("release-no-get", time.Minute)

		if lock.Release(ctx) {
			t.Fatal("expected Release() to return false when lock was never acquired")
		}
	})

	t.Run("ReleaseWrongOwner", func(t *testing.T) {
		lock1 := store.Lock("wrong-owner", time.Minute)
		defer lock1.ForceRelease(ctx)

		if !lock1.Get(ctx) {
			t.Fatal("expected lock1.Get(ctx) to succeed")
		}

		lock2 := store.Lock("wrong-owner", time.Minute)
		if lock2.Release(ctx) {
			t.Fatal("expected Release() to return false for non-owner")
		}
	})

	t.Run("Owner", func(t *testing.T) {
		lock := store.Lock("owner-test", time.Minute)

		owner := lock.Owner()
		if owner == "" {
			t.Fatal("expected Owner() to return a non-empty string")
		}

		lock2 := store.Lock("owner-test-2", time.Minute)
		if lock.Owner() == lock2.Owner() {
			t.Fatal("expected different locks to have different owners")
		}
	})

	t.Run("ForceRelease", func(t *testing.T) {
		lock1 := store.Lock("force-release", time.Minute)
		if !lock1.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("force-release", time.Minute)
		if lock2.Get(ctx) {
			t.Fatal("expected Get() to fail while lock is held")
		}

		if err := lock2.ForceRelease(ctx); err != nil {
			t.Fatalf("expected ForceRelease() to succeed, got %v", err)
		}

		lock3 := store.Lock("force-release", time.Minute)
		defer lock3.ForceRelease(ctx)
		if !lock3.Get(ctx) {
			t.Fatal("expected Get() to succeed after ForceRelease()")
		}
	})

	t.Run("ForceReleaseNonExistent", func(t *testing.T) {
		lock := store.Lock("force-release-nonexistent", time.Minute)
		if err := lock.ForceRelease(ctx); err != nil {
			t.Fatalf("expected ForceRelease() on non-existent key to succeed, got %v", err)
		}
	})

	t.Run("RunSuccess", func(t *testing.T) {
		lock := store.Lock("run-success", time.Minute)

		called := false
		err := lock.Run(ctx, func() {
			called = true
		})
		if err != nil {
			t.Fatalf("expected Run() to return nil, got %v", err)
		}
		if !called {
			t.Fatal("expected callback to be called")
		}

		// Lock should be released after Run
		lock2 := store.Lock("run-success", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("expected lock to be released after Run()")
		}
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		lock1 := store.Lock("run-not-acquired", time.Minute)
		defer lock1.ForceRelease(ctx)

		if !lock1.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("run-not-acquired", time.Minute)
		err := lock2.Run(ctx, func() {
			t.Fatal("callback should not be called")
		})
		if err != ErrLockNotAcquired {
			t.Fatalf("expected ErrLockNotAcquired, got %v", err)
		}
	})

	t.Run("RunPanicRecovery", func(t *testing.T) {
		lock := store.Lock("run-panic", time.Minute)

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
			lock.Run(ctx, func() {
				panic("test panic")
			})
		}()

		// Lock should still be released after panic
		lock2 := store.Lock("run-panic", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("expected lock to be released after panic in Run()")
		}
	})

	t.Run("BlockSuccess", func(t *testing.T) {
		lock := store.Lock("block-success", time.Minute)

		called := false
		err := lock.Block(ctx, time.Second, func() {
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
		lock1 := store.Lock("block-timeout", time.Minute)
		defer lock1.ForceRelease(ctx)

		if !lock1.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		lock2 := store.Lock("block-timeout", time.Minute)
		start := time.Now()
		err := lock2.Block(ctx, 250*time.Millisecond, func() {
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
		lock1 := store.Lock("block-wait", time.Minute)
		if !lock1.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		// Release after a short delay
		go func() {
			time.Sleep(200 * time.Millisecond)
			lock1.Release(ctx)
		}()

		lock2 := store.Lock("block-wait", time.Minute)
		defer lock2.ForceRelease(ctx)

		called := false
		err := lock2.Block(ctx, time.Second, func() {
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
		lock := store.Lock("block-panic", time.Minute)

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
			lock.Block(ctx, time.Second, func() {
				panic("block panic")
			})
		}()

		// Lock should be released after panic
		lock2 := store.Lock("block-panic", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("expected lock to be released after panic in Block()")
		}
	})

	t.Run("TTLExpiration", func(t *testing.T) {
		lock := store.Lock("ttl-expire", 200*time.Millisecond)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		// Should fail before expiry
		lock2 := store.Lock("ttl-expire", time.Minute)
		if lock2.Get(ctx) {
			lock2.ForceRelease(ctx)
			t.Fatal("expected Get() to fail before TTL expires")
		}

		// Wait for TTL to expire
		time.Sleep(300 * time.Millisecond)

		lock3 := store.Lock("ttl-expire", time.Minute)
		defer lock3.ForceRelease(ctx)
		if !lock3.Get(ctx) {
			t.Fatal("expected Get() to succeed after TTL expiration")
		}
	})

	t.Run("LongTTLStaysHeldForTestDuration", func(t *testing.T) {
		// Previously this test relied on TTL=0 meaning "no expiry"; that
		// behaviour is now rejected with ErrInvalidLockTTL because a
		// crashed holder would pin the lock forever (M-32). A long TTL
		// gives the same observable property within the test window.
		lock := store.Lock("no-ttl", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		// No sleep: a long-TTL lock will not expire within the test;
		// peers must observe it as held.
		lock2 := store.Lock("no-ttl", time.Minute)
		if lock2.Get(ctx) {
			lock2.ForceRelease(ctx)
			t.Fatal("expected lock to remain held while TTL has not elapsed")
		}
	})

	t.Run("ZeroTTLRejected", func(t *testing.T) {
		// A lock constructed with ttl=0 must refuse acquisition with
		// ErrInvalidLockTTL rather than silently pinning the key
		// permanently. Bool-only Get returns false.
		lock := store.Lock("zero-ttl", 0)
		defer lock.ForceRelease(ctx)

		acquired, err := lock.GetWithErr(ctx)
		if err == nil {
			t.Fatal("GetWithErr with ttl=0 must return ErrInvalidLockTTL")
		}
		if !errors.Is(err, ErrInvalidLockTTL) {
			t.Fatalf("GetWithErr err = %v; want ErrInvalidLockTTL", err)
		}
		if acquired {
			t.Fatal("must not acquire with ttl=0")
		}

		if lock.Get(ctx) {
			t.Fatal("Get with ttl=0 must return false")
		}
	})

	t.Run("NegativeTTLRejected", func(t *testing.T) {
		lock := store.Lock("neg-ttl", -time.Second)
		defer lock.ForceRelease(ctx)

		_, err := lock.GetWithErr(ctx)
		if !errors.Is(err, ErrInvalidLockTTL) {
			t.Fatalf("GetWithErr err = %v; want ErrInvalidLockTTL", err)
		}
	})

	t.Run("DifferentKeysDontInterfere", func(t *testing.T) {
		lockA := store.Lock("key-a", time.Minute)
		defer lockA.ForceRelease(ctx)
		lockB := store.Lock("key-b", time.Minute)
		defer lockB.ForceRelease(ctx)

		if !lockA.Get(ctx) {
			t.Fatal("expected lockA.Get(ctx) to succeed")
		}
		if !lockB.Get(ctx) {
			t.Fatal("expected lockB.Get(ctx) to succeed (different key)")
		}
		if !lockA.Release(ctx) {
			t.Fatal("expected lockA.Release(ctx) to succeed")
		}
		if !lockB.Release(ctx) {
			t.Fatal("expected lockB.Release(ctx) to succeed")
		}
	})

	t.Run("RestoreLockCorrectOwner", func(t *testing.T) {
		lock := store.Lock("restore-correct", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		owner := lock.Owner()
		restored := store.RestoreLock("restore-correct", owner)

		if !restored.Release(ctx) {
			t.Fatal("expected restored lock with correct owner to release successfully")
		}
	})

	t.Run("RestoreLockWrongOwner", func(t *testing.T) {
		lock := store.Lock("restore-wrong", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to succeed")
		}

		restored := store.RestoreLock("restore-wrong", "wrong-owner-token")
		if restored.Release(ctx) {
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
				lock := store.Lock("concurrent", time.Minute)
				if lock.Get(ctx) {
					atomic.AddInt32(&acquired, 1)
					time.Sleep(10 * time.Millisecond)
					lock.Release(ctx)
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
		lock := store.Lock("run-releases", time.Minute)

		err := lock.Run(ctx, func() {})
		if err != nil {
			t.Fatalf("expected Run() to succeed, got %v", err)
		}

		// Immediately try to acquire with a new lock — should succeed
		lock2 := store.Lock("run-releases", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("expected lock to be available after Run() returns")
		}
	})

	t.Run("LockWithTTLViaStore", func(t *testing.T) {
		lock := store.Lock("store-ttl", 5*time.Second)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("expected Get() to succeed with TTL")
		}

		// Verify the lock is held
		lock2 := store.Lock("store-ttl", time.Minute)
		if lock2.Get(ctx) {
			lock2.ForceRelease(ctx)
			t.Fatal("expected lock to be held")
		}
	})

	t.Run("InstanceIsolation", func(t *testing.T) {
		store2 := NewMemoryStore("test_lock_2")
		store2.Start()
		defer func() { _ = store2.Shutdown(context.Background()) }()

		lock1 := store.Lock("isolated-key", time.Minute)
		defer lock1.ForceRelease(ctx)

		if !lock1.Get(ctx) {
			t.Fatal("expected store1 lock to succeed")
		}

		// Different store instance should have its own lock store
		lock2 := store2.Lock("isolated-key", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("expected store2 lock to succeed (separate lock store)")
		}
	})

	t.Run("GetWithErr", func(t *testing.T) {
		lock := store.Lock("get-with-err", time.Minute)
		defer lock.ForceRelease(ctx)

		acquired, err := lock.GetWithErr(ctx)
		if err != nil {
			t.Fatalf("MemoryLock.GetWithErr must never return a backend error; got %v", err)
		}
		if !acquired {
			t.Fatal("expected first GetWithErr() to acquire")
		}

		// Second call on same lock instance returns (false, nil) -- the
		// lock is held by the same owner, but Get's semantics treat
		// "already held" as "not acquired by this call" (matches Get).
		acquired2, err := lock.GetWithErr(ctx)
		if err != nil {
			t.Fatalf("MemoryLock.GetWithErr must never return a backend error; got %v", err)
		}
		if acquired2 {
			t.Fatal("expected second GetWithErr() to return false (already held)")
		}
	})

	t.Run("GetWithErr_CancelledCtx", func(t *testing.T) {
		lock := store.Lock("get-with-err-cancel", time.Minute)
		defer lock.ForceRelease(ctx)

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		acquired, err := lock.GetWithErr(cancelled)
		// Memory backend has no I/O, so a pre-cancelled ctx returns
		// (false, nil) -- there is no backend operation to surface a
		// real error from.
		if err != nil {
			t.Fatalf("MemoryLock.GetWithErr on cancelled ctx must return nil err (no I/O); got %v", err)
		}
		if acquired {
			t.Fatal("expected acquired=false on cancelled ctx")
		}
	})
}

// TestMemoryLock_BlockHonorsCtxCancel verifies that Block wakes on ctx cancel
// instead of waiting out the timeout.
func TestMemoryLock_BlockHonorsCtxCancel(t *testing.T) {
	store := NewMemoryStore("ctx_cancel_block")
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	// Hold the lock so Block has to wait.
	holder := store.Lock("held", time.Minute)
	if !holder.Get(context.Background()) {
		t.Fatal("failed to acquire holder lock")
	}
	defer holder.Release(context.Background())

	blocker := store.Lock("held", time.Minute)
	cancelCtx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := blocker.Block(cancelCtx, 10*time.Second, func() {
		t.Error("callback should not run when ctx is cancelled before acquisition")
	})
	elapsed := time.Since(start)

	if err != context.Canceled {
		t.Fatalf("Block err = %v; expected context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Block took %v after ctx cancel; expected prompt return", elapsed)
	}
}

// TestMemoryLock_MethodsRespectPreCancelledCtx verifies each method reports
// ctx cancellation without mutating lock state.
func TestMemoryLock_MethodsRespectPreCancelledCtx(t *testing.T) {
	store := NewMemoryStore("ctx_cancel_methods")
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	lock := store.Lock("key", time.Minute)
	if lock.Get(cancelled) {
		t.Error("Get with cancelled ctx acquired the lock; expected false")
	}
	if lock.Release(cancelled) {
		t.Error("Release with cancelled ctx reported success; expected false")
	}
	if err := lock.ForceRelease(cancelled); err == nil {
		t.Error("ForceRelease with cancelled ctx returned nil; expected ctx.Err()")
	}
}
