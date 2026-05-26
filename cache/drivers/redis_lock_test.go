package drivers

import (
	"context"
	"errors"
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
	store, err := NewRedisStore(context.Background(), "test_lock", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		mr.Close()
		t.Fatalf("failed to create redis store: %v", err)
	}
	cleanup := func() {
		_ = store.Shutdown(context.Background())
		mr.Close()
	}
	return store, mr, cleanup
}

func TestRedisLock(t *testing.T) {
	ctx := context.Background()

	t.Run("Get", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
		acquired := lock.Get(ctx)
		if !acquired {
			t.Error("Get() = false, want true")
		}
	})

	t.Run("DoubleGet", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock1 := store.Lock("resource1", time.Minute)
		lock2 := store.Lock("resource1", time.Minute)

		if !lock1.Get(ctx) {
			t.Fatal("first Get() = false, want true")
		}
		if lock2.Get(ctx) {
			t.Error("second Get() = true, want false (lock already held)")
		}
	})

	t.Run("Release", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
		if !lock.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}
		if !lock.Release(ctx) {
			t.Error("Release() = false, want true")
		}

		// Can reacquire after release
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("Get() after Release() = false, want true")
		}
	})

	t.Run("ReleaseWrongOwner", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
		if !lock.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		// Restore with a wrong owner
		wrongOwner := store.RestoreLock("resource1", "wrong-owner-token")
		if wrongOwner.Release(ctx) {
			t.Error("Release() with wrong owner = true, want false")
		}
	})

	t.Run("Run", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
		called := false
		err := lock.Run(ctx, func() {
			called = true
		})
		if err != nil {
			t.Errorf("Run() error = %v, want nil", err)
		}
		if !called {
			t.Error("callback was not called")
		}

		// Lock should be released after Run
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("lock not released after Run()")
		}
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		holder := store.Lock("resource1", time.Minute)
		if !holder.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		lock := store.Lock("resource1", time.Minute)
		err := lock.Run(ctx, func() {
			t.Error("callback should not be called when lock not acquired")
		})
		if err != ErrLockNotAcquired {
			t.Errorf("Run() error = %v, want ErrLockNotAcquired", err)
		}
	})

	t.Run("Block", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		holder := store.Lock("resource1", time.Minute)
		if !holder.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		// Release the lock from another goroutine after a short delay
		go func() {
			time.Sleep(150 * time.Millisecond)
			holder.Release(ctx)
		}()

		lock := store.Lock("resource1", time.Minute)
		called := false
		err := lock.Block(ctx, 2*time.Second, func() {
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

		holder := store.Lock("resource1", time.Minute)
		if !holder.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		lock := store.Lock("resource1", time.Minute)
		err := lock.Block(ctx, 250*time.Millisecond, func() {
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
		if !lock.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		// Lock should still be held before TTL expires
		lock2 := store.Lock("resource1", time.Minute)
		if lock2.Get(ctx) {
			t.Error("Get() before TTL = true, want false")
		}

		// Fast-forward past the TTL
		mr.FastForward(3 * time.Second)

		// Lock should have expired, a new lock can be acquired
		lock3 := store.Lock("resource1", time.Minute)
		if !lock3.Get(ctx) {
			t.Error("Get() after TTL expiration = false, want true")
		}
	})

	t.Run("Owner", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
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

		lock := store.Lock("resource1", time.Minute)
		if !lock.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}

		// Force release from a different lock instance (different owner)
		other := store.Lock("resource1", time.Minute)
		err := other.ForceRelease(ctx)
		if err != nil {
			t.Errorf("ForceRelease() error = %v, want nil", err)
		}

		// Lock should be released; a new one can be acquired
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("Get() after ForceRelease() = false, want true")
		}
	})

	t.Run("RestoreLock", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)
		if !lock.Get(ctx) {
			t.Fatal("Get() = false, want true")
		}
		owner := lock.Owner()

		// Restore with the correct owner
		restored := store.RestoreLock("resource1", owner)
		if !restored.Release(ctx) {
			t.Error("restored lock Release() = false, want true")
		}

		// Lock should be released now
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("Get() after restored Release() = false, want true")
		}
	})

	t.Run("PanicRecovery", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)

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

		// Lock should be released despite the panic
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("Get() after panic in Run() = false, want true (lock should be released)")
		}
	})

	t.Run("PanicRecoveryBlock", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("resource1", time.Minute)

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
			lock.Block(ctx, time.Second, func() {
				panic("test panic in block")
			})
		}()

		// Lock should be released despite the panic
		lock2 := store.Lock("resource1", time.Minute)
		if !lock2.Get(ctx) {
			t.Error("Get() after panic in Block() = false, want true (lock should be released)")
		}
	})

	t.Run("DifferentKeys", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock1 := store.Lock("resource1", time.Minute)
		lock2 := store.Lock("resource2", time.Minute)

		if !lock1.Get(ctx) {
			t.Error("Get() on resource1 = false, want true")
		}
		if !lock2.Get(ctx) {
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

				lock := store.Lock("shared-resource", time.Minute)
				err := lock.Block(ctx, 5*time.Second, func() {
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

	t.Run("GetWithErr_Acquire", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("get-with-err", time.Minute)
		acquired, err := lock.GetWithErr(ctx)
		if err != nil {
			t.Fatalf("healthy Redis must return nil err, got %v", err)
		}
		if !acquired {
			t.Error("expected acquired=true on first GetWithErr")
		}
	})

	t.Run("GetWithErr_Contention", func(t *testing.T) {
		store, _, cleanup := setupRedisForLock(t)
		defer cleanup()

		l1 := store.Lock("contended", time.Minute)
		if !l1.Get(ctx) {
			t.Fatal("first acquire failed")
		}
		l2 := store.Lock("contended", time.Minute)
		acquired, err := l2.GetWithErr(ctx)
		// Contention is (false, nil) -- the SETNX command succeeded
		// but didn't set the key. Backend error must NOT leak here.
		if err != nil {
			t.Fatalf("contention must return nil err, got %v", err)
		}
		if acquired {
			t.Error("expected acquired=false on contended acquire")
		}
	})

	t.Run("GetWithErr_BackendDown", func(t *testing.T) {
		// This is the C-04-fb4 canonical regression: a Redis outage
		// must surface as (any, err != nil), NOT (false, nil).
		// Otherwise callers cannot distinguish "another holder has
		// the lock" (healthy) from "Redis is down" (outage).
		store, mr, cleanup := setupRedisForLock(t)
		defer cleanup()

		lock := store.Lock("backend-down", time.Minute)
		// Tear down the Redis server mid-flight to force a backend
		// error on the next SETNX.
		mr.Close()

		_, err := lock.GetWithErr(ctx)
		if err == nil {
			t.Fatal("backend down must surface as non-nil error from GetWithErr")
		}
		// We don't assert the exact error string -- go-redis wraps
		// net errors differently across versions. The contract is
		// "non-nil error".
	})
}

// TestRedisLock_GetHonorsCtxCancel verifies that a cancelled ctx propagates into
// the underlying Redis SETNX so Get returns promptly rather than blocking on
// context.Background() inside the driver.
func TestRedisLock_GetHonorsCtxCancel(t *testing.T) {
	store, _, cleanup := setupRedisForLock(t)
	defer cleanup()

	lock := store.Lock("ctx-get", time.Minute)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	acquired := lock.Get(cancelled)
	elapsed := time.Since(start)

	if acquired {
		t.Fatal("Get with cancelled ctx returned true; expected false — ctx was ignored")
	}
	if elapsed > time.Second {
		t.Fatalf("Get with cancelled ctx took %v; expected prompt return — ctx was ignored", elapsed)
	}
}

// TestRedisLock_BlockHonorsCtxCancel verifies that Block wakes on ctx cancellation
// instead of continuing to poll until the timeout expires.
func TestRedisLock_BlockHonorsCtxCancel(t *testing.T) {
	store, _, cleanup := setupRedisForLock(t)
	defer cleanup()

	// Hold the lock so Block has to wait.
	holder := store.Lock("ctx-block", time.Minute)
	if !holder.Get(context.Background()) {
		t.Fatal("failed to acquire holder lock")
	}
	defer holder.Release(context.Background())

	blocker := store.Lock("ctx-block", time.Minute)
	cancelCtx, cancel := context.WithCancel(context.Background())

	// Cancel after a short delay; Block should return ctx.Err() — not wait out the full 10s timeout.
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := blocker.Block(cancelCtx, 10*time.Second, func() {
		t.Error("callback should not run when ctx is cancelled before acquisition")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Block with cancelled ctx returned nil; expected ctx.Err()")
	}
	if err != context.Canceled {
		t.Fatalf("Block err = %v; expected context.Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Block took %v after ctx cancel; expected prompt return", elapsed)
	}
}

// TestRedisLock_ReleaseHonorsCtxCancel verifies that Release propagates ctx to
// the Redis EVAL rather than silently using context.Background().
func TestRedisLock_ReleaseHonorsCtxCancel(t *testing.T) {
	store, _, cleanup := setupRedisForLock(t)
	defer cleanup()

	lock := store.Lock("ctx-release", time.Minute)
	if !lock.Get(context.Background()) {
		t.Fatal("failed to acquire lock")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	ok := lock.Release(cancelled)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("Release with cancelled ctx returned true; expected false — ctx was ignored")
	}
	if elapsed > time.Second {
		t.Fatalf("Release with cancelled ctx took %v; expected prompt return — ctx was ignored", elapsed)
	}
}

// TestRedisLock_ForceReleaseHonorsCtxCancel verifies that ForceRelease propagates
// ctx to the Redis DEL rather than silently using context.Background().
func TestRedisLock_ForceReleaseHonorsCtxCancel(t *testing.T) {
	store, _, cleanup := setupRedisForLock(t)
	defer cleanup()

	lock := store.Lock("ctx-force-release", time.Minute)
	if !lock.Get(context.Background()) {
		t.Fatal("failed to acquire lock")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	start := time.Now()
	err := lock.ForceRelease(cancelled)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ForceRelease with cancelled ctx returned nil; expected ctx.Err() — ctx was ignored")
	}
	if elapsed > time.Second {
		t.Fatalf("ForceRelease with cancelled ctx took %v; expected prompt return — ctx was ignored", elapsed)
	}
}

// TestRedisLock_ZeroTTLRejected pins the M-32 contract on Redis: a lock
// constructed with ttl<=0 must refuse acquisition with ErrInvalidLockTTL
// rather than send SET NX with PX=0 (which Redis treats as "never
// expires"). A crashed holder with no expiry pins the key indefinitely.
func TestRedisLock_ZeroTTLRejected(t *testing.T) {
	store, _, cleanup := setupRedisForLock(t)
	defer cleanup()
	ctx := context.Background()

	lock := store.Lock("redis-zero-ttl", 0)
	defer lock.ForceRelease(ctx)

	acquired, err := lock.GetWithErr(ctx)
	if !errors.Is(err, ErrInvalidLockTTL) {
		t.Fatalf("GetWithErr err = %v; want ErrInvalidLockTTL", err)
	}
	if acquired {
		t.Fatal("must not acquire with ttl=0")
	}
	if lock.Get(ctx) {
		t.Fatal("Get with ttl=0 must return false")
	}
}
