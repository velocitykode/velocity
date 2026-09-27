//go:build unix

package drivers

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pauseExpiredRemoval makes s run write before it removes an expired
// entry: write runs in a goroutine and is given a moment to land. The
// returned func waits for write to finish.
func pauseExpiredRemoval(t *testing.T, s *FileStore, write func() error) (wait func()) {
	t.Helper()
	done := make(chan error, 1)
	var once sync.Once
	s.expiredRemoveHook = func() {
		once.Do(func() {
			go func() { done <- write() }()
			select {
			case err := <-done:
				done <- err
			case <-time.After(200 * time.Millisecond):
				// The write waits for the key's write lock.
			}
		})
	}
	return func() {
		if err := <-done; err != nil {
			t.Fatalf("write during expired removal: %v", err)
		}
	}
}

// A read of an expired entry on instance 1 removes it; a write of a fresh
// value by instance 2 between the read's expiry check and its removal
// survives.
func TestFileStore_GetExpiredKeepsConcurrentFreshWrite(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	writeExpiredFile(t, stores[0], "session", "old")

	wait := pauseExpiredRemoval(t, stores[0], func() error {
		return stores[1].PutCtx(ctx, "session", "fresh", time.Hour)
	})
	if v, ok := stores[0].GetCtx(ctx, "session"); ok {
		t.Fatalf("GetCtx of the expired entry = %v; want a miss", v)
	}
	wait()

	if v, ok := stores[1].GetCtx(ctx, "session"); !ok || v != "fresh" {
		t.Fatalf("GetCtx after the fresh write = (%v, %v); want (fresh, true)", v, ok)
	}
}

// The expiry sweep on instance 1 removes an expired entry; a write of a
// fresh value by instance 2 between the sweep's expiry check and its
// removal survives.
func TestFileStore_SweepKeepsConcurrentFreshWrite(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	writeExpiredFile(t, stores[0], "session", "old")

	wait := pauseExpiredRemoval(t, stores[0], func() error {
		return stores[1].PutCtx(ctx, "session", "fresh", time.Hour)
	})
	stores[0].sweepExpired()
	wait()

	if v, ok := stores[1].GetCtx(ctx, "session"); !ok || v != "fresh" {
		t.Fatalf("GetCtx after the fresh write = (%v, %v); want (fresh, true)", v, ok)
	}
}

// The sweep skips an expired entry whose key's write lock is held instead
// of waiting for it; a later sweep removes it.
func TestFileStore_SweepSkipsEntryWithHeldKeyLock(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()
	writeExpiredFile(t, stores[0], "busy", "old")

	unlock, err := stores[1].lockKeyForWrite(ctx, "busy")
	if err != nil {
		t.Fatalf("lockKeyForWrite: %v", err)
	}
	done := make(chan struct{})
	go func() { stores[0].sweepExpired(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		unlock()
		t.Fatal("sweep waited for a held key write lock")
	}
	path := stores[0].getCacheFilePath("busy")
	if !fileExists(path) {
		unlock()
		t.Fatal("sweep removed an entry whose key write lock was held")
	}
	unlock()

	stores[0].sweepExpired()
	if fileExists(path) {
		t.Fatal("sweep left an expired entry with a free key write lock")
	}
}

// Instance 1 sweeps and reads in loops while instance 2 turns a key from
// an expired entry into a fresh one; a read right after the fresh write
// always finds it.
func TestFileStore_ExpiredRemovalNeverDropsFreshWrites(t *testing.T) {
	stores := newSharedFileStores(t, 2)
	ctx := context.Background()

	stop := make(chan struct{})
	var stopOnce sync.Once
	var wg sync.WaitGroup
	stopWorkers := func() {
		stopOnce.Do(func() { close(stop) })
		wg.Wait()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				stores[0].sweepExpired()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				stores[0].GetCtx(ctx, fmt.Sprintf("k%d", i%4))
			}
		}
	}()
	// A t.Fatalf below ends this goroutine only; stop and join the
	// workers before the stores and their directory are cleaned up.
	t.Cleanup(stopWorkers)

	var misses atomic.Int32
	deadline := time.Now().Add(2 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		key := fmt.Sprintf("k%d", i%4)
		writeExpiredFile(t, stores[1], key, "old")
		want := fmt.Sprintf("v%d", i)
		if err := stores[1].PutCtx(ctx, key, want, time.Hour); err != nil {
			t.Fatalf("PutCtx: %v", err)
		}
		if v, ok := stores[1].GetCtx(ctx, key); !ok || v != want {
			misses.Add(1)
		}
	}
	stopWorkers()
	if n := misses.Load(); n > 0 {
		t.Fatalf("%d reads after a fresh write missed it", n)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
