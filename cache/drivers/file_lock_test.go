//go:build unix

package drivers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFileLock covers the full Lock contract on the file driver.
// Mirrors TestMemoryLock so any divergence between drivers is loud.
func TestFileLock(t *testing.T) {
	t.Parallel()
	store, err := NewFileStore("filelock-test", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	ctx := context.Background()

	t.Run("GetAndRelease", func(t *testing.T) {
		lock := store.Lock("get-release", time.Minute)
		if lock == nil {
			t.Fatal("FileStore.Lock must not return nil on POSIX")
		}
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("Get() returned false on first call")
		}
		if !lock.Release(ctx) {
			t.Fatal("Release() returned false for owner")
		}
	})

	t.Run("DoubleGet", func(t *testing.T) {
		lock := store.Lock("double-get", time.Minute)
		defer lock.ForceRelease(ctx)

		if !lock.Get(ctx) {
			t.Fatal("first Get() must succeed")
		}
		if lock.Get(ctx) {
			t.Fatal("second Get() on same lock must return false")
		}
	})

	t.Run("ContentionAcrossInstances", func(t *testing.T) {
		lock1 := store.Lock("contend", time.Minute)
		defer lock1.ForceRelease(ctx)
		if !lock1.Get(ctx) {
			t.Fatal("lock1.Get must succeed")
		}

		lock2 := store.Lock("contend", time.Minute)
		if lock2.Get(ctx) {
			lock2.ForceRelease(ctx)
			t.Fatal("lock2 must not acquire while lock1 holds")
		}

		if !lock1.Release(ctx) {
			t.Fatal("lock1.Release must succeed")
		}
		if !lock2.Get(ctx) {
			t.Fatal("lock2 must acquire after lock1 released")
		}
		lock2.Release(ctx)
	})

	t.Run("ReleaseWrongOwner", func(t *testing.T) {
		lock1 := store.Lock("wrong-owner", time.Minute)
		defer lock1.ForceRelease(ctx)
		if !lock1.Get(ctx) {
			t.Fatal("lock1.Get must succeed")
		}
		// Restore with a different owner token; release must refuse.
		other := store.RestoreLock("wrong-owner", "not-the-owner")
		if other.Release(ctx) {
			t.Fatal("Release with mismatched owner must return false")
		}
	})

	t.Run("ForceRelease", func(t *testing.T) {
		lock1 := store.Lock("force", time.Minute)
		if !lock1.Get(ctx) {
			t.Fatal("lock1.Get must succeed")
		}

		// A peer lock without the owner credential can still ForceRelease.
		lock2 := store.Lock("force", time.Minute)
		if err := lock2.ForceRelease(ctx); err != nil {
			t.Fatalf("ForceRelease must succeed: %v", err)
		}

		// After ForceRelease, a new acquire must succeed.
		lock3 := store.Lock("force", time.Minute)
		defer lock3.ForceRelease(ctx)
		if !lock3.Get(ctx) {
			t.Fatal("Get after ForceRelease must succeed")
		}
	})

	t.Run("RunSuccess", func(t *testing.T) {
		lock := store.Lock("run-success", time.Minute)
		called := false
		if err := lock.Run(ctx, func() { called = true }); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !called {
			t.Fatal("callback not invoked")
		}
	})

	t.Run("RunNotAcquired", func(t *testing.T) {
		lock1 := store.Lock("run-not-acquired", time.Minute)
		defer lock1.ForceRelease(ctx)
		if !lock1.Get(ctx) {
			t.Fatal("lock1.Get must succeed")
		}

		lock2 := store.Lock("run-not-acquired", time.Minute)
		err := lock2.Run(ctx, func() { t.Fatal("must not run") })
		if err != ErrLockNotAcquired {
			t.Fatalf("Run err = %v; want ErrLockNotAcquired", err)
		}
	})

	t.Run("RunPanicRecovery", func(t *testing.T) {
		lock := store.Lock("run-panic", time.Minute)

		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Fatal("panic must propagate")
				}
			}()
			lock.Run(ctx, func() { panic("boom") })
		}()

		// Lock must be released even after panic.
		lock2 := store.Lock("run-panic", time.Minute)
		defer lock2.ForceRelease(ctx)
		if !lock2.Get(ctx) {
			t.Fatal("lock must be available after panic in Run")
		}
	})

	t.Run("BlockSuccess", func(t *testing.T) {
		lock := store.Lock("block-success", time.Minute)
		called := false
		if err := lock.Block(ctx, time.Second, func() { called = true }); err != nil {
			t.Fatalf("Block: %v", err)
		}
		if !called {
			t.Fatal("callback not invoked")
		}
	})

	t.Run("BlockTimeout", func(t *testing.T) {
		lock1 := store.Lock("block-timeout", time.Minute)
		defer lock1.ForceRelease(ctx)
		if !lock1.Get(ctx) {
			t.Fatal("lock1.Get must succeed")
		}
		lock2 := store.Lock("block-timeout", time.Minute)
		err := lock2.Block(ctx, 250*time.Millisecond, func() {
			t.Fatal("must not run")
		})
		if err != ErrLockTimeout {
			t.Fatalf("Block err = %v; want ErrLockTimeout", err)
		}
	})

	t.Run("ConcurrentMutualExclusion", func(t *testing.T) {
		const goroutines = 30
		var acquired int32
		var concurrent int32
		var maxConcurrent int32
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				lock := store.Lock("mutex", time.Minute)
				if lock.Get(ctx) {
					inFlight := atomic.AddInt32(&concurrent, 1)
					for {
						cur := atomic.LoadInt32(&maxConcurrent)
						if inFlight <= cur || atomic.CompareAndSwapInt32(&maxConcurrent, cur, inFlight) {
							break
						}
					}
					atomic.AddInt32(&acquired, 1)
					time.Sleep(2 * time.Millisecond)
					atomic.AddInt32(&concurrent, -1)
					lock.Release(ctx)
				}
			}()
		}
		wg.Wait()
		if atomic.LoadInt32(&acquired) == 0 {
			t.Fatal("at least one goroutine must acquire")
		}
		if got := atomic.LoadInt32(&maxConcurrent); got > 1 {
			t.Fatalf("file lock must enforce mutual exclusion; max concurrent holders = %d", got)
		}
	})

	t.Run("RestoreLockOwnerMatch", func(t *testing.T) {
		lock := store.Lock("restore-ok", time.Minute)
		if !lock.Get(ctx) {
			t.Fatal("Get must succeed")
		}
		owner := lock.Owner()
		restored := store.RestoreLock("restore-ok", owner)
		if !restored.Release(ctx) {
			t.Fatal("Release on restored lock with matching owner must succeed")
		}
	})

	t.Run("GetWithErr", func(t *testing.T) {
		lock := store.Lock("get-with-err", time.Minute)
		defer lock.ForceRelease(ctx)
		got, err := lock.GetWithErr(ctx)
		if err != nil {
			t.Fatalf("GetWithErr: %v", err)
		}
		if !got {
			t.Fatal("expected acquired=true on first GetWithErr")
		}
	})

	t.Run("ZeroTTLRejected", func(t *testing.T) {
		lock := store.Lock("file-zero-ttl", 0)
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
	})
}
