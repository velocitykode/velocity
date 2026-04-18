package drivers

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemoryStore_ForgetDuringRemember exercises the race where one goroutine
// is inside Remember's callback (just computed a value, about to Put) while
// another goroutine calls Forget on the same key. Both must complete without
// panic; the final state is either "key exists with value" or "key absent",
// both of which are legal — the only illegal outcome is a data race, which
// -race would surface.
func TestMemoryStore_ForgetDuringRemember(t *testing.T) {
	store := NewMemoryStore("")
	store.Start()
	defer store.Close()

	const iters = 200
	var wg sync.WaitGroup
	var callbackCalls atomic.Int32

	for i := 0; i < iters; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := store.Remember("contested", time.Hour, func() interface{} {
				callbackCalls.Add(1)
				return "value"
			})
			if err != nil {
				t.Errorf("Remember: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := store.Forget("contested"); err != nil {
				t.Errorf("Forget: %v", err)
			}
		}()
	}
	wg.Wait()
	// Sanity: the callback fired at least once over 200 rounds — if it
	// never fires the store's Put is broken and Remember would never cache.
	if callbackCalls.Load() == 0 {
		t.Error("Remember callback never fired; store.Put likely broken")
	}
}

// TestFileStore_CorruptedValueReturnsMiss verifies that a cache file with
// invalid JSON is treated as a miss, not as a hard error. This prevents a
// corrupted entry from poisoning the cache for the lifetime of the process.
func TestFileStore_CorruptedValueReturnsMiss(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore("", dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if err := store.Put("good", "value", time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := store.getCacheFilePath("good")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}

	if _, found := store.Get("good"); found {
		t.Error("Get on corrupted file should return found=false, not surface the parse error")
	}
}

// TestFileStore_PermissionDeniedPut verifies that a Put into a directory the
// process can't write to errors rather than silently succeeding. Skipped on
// Windows and as root — same reasons as storage/errorpaths_test.go.
func TestFileStore_PermissionDeniedPut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission tests don't apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission checks")
	}

	root := t.TempDir()
	readOnly := filepath.Join(root, "readonly-cache")
	if err := os.MkdirAll(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	// Skip NewFileStore's MkdirAll by pointing at an already-existing dir.
	store, err := NewFileStore("", readOnly)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if err := store.Put("blocked", "value", time.Hour); err == nil {
		t.Error("Put into a read-only cache dir should error")
	}
}

// TestMemoryStore_ConcurrentIncrement ensures Increment is race-free. A
// mutation that drops the mutex would surface under -race.
func TestMemoryStore_ConcurrentIncrement(t *testing.T) {
	store := NewMemoryStore("")
	store.Start()
	defer store.Close()

	const workers = 8
	const iters = 50
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := store.Increment("counter", 1); err != nil {
					t.Errorf("Increment: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	val, ok := store.Get("counter")
	if !ok {
		t.Fatal("counter should exist after increments")
	}
	got, _ := val.(int64)
	if want := int64(workers * iters); got != want {
		t.Errorf("counter = %d, want %d (concurrent Increment must be atomic)", got, want)
	}
}
