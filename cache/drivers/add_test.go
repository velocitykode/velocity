package drivers

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// TestMemoryStore_Add covers the SETNX-style atomic add on the memory driver.
func TestMemoryStore_Add(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore("addtest")
	store.Start()
	defer func() { _ = store.Shutdown(context.Background()) }()

	t.Run("InsertsWhenAbsent", func(t *testing.T) {
		inserted, err := store.Add("absent", "v", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if !inserted {
			t.Fatal("Add must return true when the key is absent")
		}
		got, ok := store.Get("absent")
		if !ok || got != "v" {
			t.Fatalf("Get after Add = (%v,%v), want (v,true)", got, ok)
		}
	})

	t.Run("RejectsWhenPresent", func(t *testing.T) {
		if err := store.Put("present", "first", time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
		inserted, err := store.Add("present", "second", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if inserted {
			t.Fatal("Add must return false when the key already exists")
		}
		got, _ := store.Get("present")
		if got != "first" {
			t.Errorf("Add must not overwrite; Get = %v, want first", got)
		}
	})

	t.Run("ReplacesExpiredEntry", func(t *testing.T) {
		if err := store.Put("expired", "old", 20*time.Millisecond); err != nil {
			t.Fatalf("Put: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		inserted, err := store.Add("expired", "new", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if !inserted {
			t.Fatal("Add must treat expired entries as absent")
		}
		got, _ := store.Get("expired")
		if got != "new" {
			t.Errorf("Get = %v, want new", got)
		}
	})

	t.Run("ExactlyOneWinnerUnderConcurrency", func(t *testing.T) {
		const goroutines = 100
		var winners int32
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				ok, err := store.Add("contended", "v", time.Hour)
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				if ok {
					atomic.AddInt32(&winners, 1)
				}
			}()
		}
		wg.Wait()
		if got := atomic.LoadInt32(&winners); got != 1 {
			t.Fatalf("exactly one Add must win under concurrency; got %d winners", got)
		}
	})
}

// TestFileStore_Add covers the SETNX-style atomic add on the file driver.
func TestFileStore_Add(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store, err := NewFileStoreWithOptions("addtest", filepath.Join(dir, "cache"), time.Hour)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	t.Run("InsertsWhenAbsent", func(t *testing.T) {
		inserted, err := store.Add("absent", "v", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if !inserted {
			t.Fatal("Add must return true when the key is absent")
		}
	})

	t.Run("RejectsWhenPresent", func(t *testing.T) {
		if err := store.Put("present", "first", time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
		inserted, err := store.Add("present", "second", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if inserted {
			t.Fatal("Add must return false when the key already exists")
		}
		got, _ := store.Get("present")
		if got != "first" {
			t.Errorf("Add must not overwrite; Get = %v, want first", got)
		}
	})

	t.Run("ExactlyOneWinnerUnderConcurrency", func(t *testing.T) {
		const goroutines = 50
		var winners int32
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				ok, err := store.Add("file-contended", "v", time.Hour)
				if err != nil {
					t.Errorf("Add: %v", err)
					return
				}
				if ok {
					atomic.AddInt32(&winners, 1)
				}
			}()
		}
		wg.Wait()
		if got := atomic.LoadInt32(&winners); got != 1 {
			t.Fatalf("exactly one Add must win under concurrency; got %d winners", got)
		}
	})
}

// TestFileStore_Add_CrossInstanceMutualExclusion exercises the
// cross-process atomicity contract by pointing two independent
// FileStore instances at the same directory and racing them on Add
// for the same key. The O_CREATE|O_EXCL atomic-create gate must elect
// exactly one winner regardless of which instance asks first; without
// it the prior best-effort code could let both instances observe the
// key as absent and both succeed.
func TestFileStore_Add_CrossInstanceMutualExclusion(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "shared-cache")

	storeA, err := NewFileStoreWithOptions("xinst", dir, time.Hour)
	if err != nil {
		t.Fatalf("storeA: %v", err)
	}
	defer func() { _ = storeA.Shutdown(context.Background()) }()
	storeB, err := NewFileStoreWithOptions("xinst", dir, time.Hour)
	if err != nil {
		t.Fatalf("storeB: %v", err)
	}
	defer func() { _ = storeB.Shutdown(context.Background()) }()

	const iterations = 30
	for i := 0; i < iterations; i++ {
		key := fmt.Sprintf("xkey-%d", i)
		var aOK, bOK bool
		var aErr, bErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); aOK, aErr = storeA.Add(key, "from-a", time.Hour) }()
		go func() { defer wg.Done(); bOK, bErr = storeB.Add(key, "from-b", time.Hour) }()
		wg.Wait()
		if aErr != nil || bErr != nil {
			t.Fatalf("iter %d: Add errors aErr=%v bErr=%v", i, aErr, bErr)
		}
		if aOK == bOK {
			t.Fatalf("iter %d: cross-instance Add must elect exactly one winner; aOK=%v bOK=%v", i, aOK, bOK)
		}
	}
}

// TestRedisStore_Add covers the SETNX-based Add primitive on the Redis driver.
func TestRedisStore_Add(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	defer mr.Close()
	store, err := NewRedisStore(context.Background(), "addtest", mr.Host(), mr.Server().Addr().Port, "", 0, false)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	defer func() { _ = store.Shutdown(context.Background()) }()

	t.Run("InsertsWhenAbsent", func(t *testing.T) {
		inserted, err := store.Add("k1", "v", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if !inserted {
			t.Fatal("Add must return true when the key is absent")
		}
	})

	t.Run("RejectsWhenPresent", func(t *testing.T) {
		if err := store.Put("k2", "first", time.Hour); err != nil {
			t.Fatalf("Put: %v", err)
		}
		inserted, err := store.Add("k2", "second", time.Hour)
		if err != nil {
			t.Fatalf("Add: %v", err)
		}
		if inserted {
			t.Fatal("Add must return false when the key already exists")
		}
		got, _ := store.Get("k2")
		if got != "first" {
			t.Errorf("Add must not overwrite; Get = %v, want first", got)
		}
	})

	t.Run("AddCtxRespectsCancelledCtx", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := store.AddCtx(ctx, "ctx-cancel", "v", time.Hour)
		if err == nil {
			t.Fatal("AddCtx on cancelled ctx must surface an error")
		}
	})
}
