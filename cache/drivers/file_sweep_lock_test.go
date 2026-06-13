package drivers

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// writeExpiredFile seeds an already-expired cache file directly on disk.
// A negative ttl now means "store forever" per the Store contract, so it can
// no longer be used as a shortcut to create an expired entry; these sweep
// tests need a genuinely past-deadline file, so write one explicitly.
func writeExpiredFile(t *testing.T, s *FileStore, key, value string) {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	valueData, err := MarshalValue(value)
	if err != nil {
		t.Fatalf("MarshalValue: %v", err)
	}
	raw, err := json.Marshal(fileCacheItem{Value: valueData, Expiration: &past})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(s.getCacheFilePath(key), raw, cacheFileMode); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// TestFileStore_SweepWalkDoesNotHoldLock proves the cleanup walk runs
// without the store mutex: the walk hook (invoked once per visited file,
// mid-walk) must be able to TryLock the write lock and serve a concurrent
// Get. Before the restructure the walk held s.mu.Lock() for its entire
// duration, so both would have failed.
func TestFileStore_SweepWalkDoesNotHoldLock(t *testing.T) {
	s, err := NewFileStore("sweeplock", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	if err := s.Put("fresh", "alive", time.Hour); err != nil {
		t.Fatalf("Put fresh: %v", err)
	}
	writeExpiredFile(t, s, "expired", "dead")

	hookRuns := 0
	s.walkHook = func() {
		hookRuns++
		if !s.mu.TryLock() {
			t.Error("store mutex held during cleanup walk")
		} else {
			s.mu.Unlock()
		}
		if v, ok := s.GetCtx(context.Background(), "fresh"); !ok || v != "alive" {
			t.Errorf("Get during cleanup walk = (%v, %v), want (alive, true)", v, ok)
		}
	}

	s.sweepExpired()

	if hookRuns == 0 {
		t.Fatal("walk hook never ran; sweep did not visit any files")
	}
	if _, ok := s.Get("expired"); ok {
		t.Error("expired entry survived sweep")
	}
	if v, ok := s.Get("fresh"); !ok || v != "alive" {
		t.Errorf("fresh entry after sweep = (%v, %v), want (alive, true)", v, ok)
	}
}

// TestFileStore_FlushWalkDoesNotHoldLock proves the FlushCtx collection walk
// runs without the store mutex, mirroring the sweep test above.
func TestFileStore_FlushWalkDoesNotHoldLock(t *testing.T) {
	s, err := NewFileStore("flushlock", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	for _, key := range []string{"a", "b", "c"} {
		if err := s.Put(key, "v", time.Hour); err != nil {
			t.Fatalf("Put %s: %v", key, err)
		}
	}

	hookRuns := 0
	s.walkHook = func() {
		hookRuns++
		if !s.mu.TryLock() {
			t.Error("store mutex held during flush walk")
		} else {
			s.mu.Unlock()
		}
		// During the collection walk nothing has been deleted yet, so a
		// concurrent Get must still be served.
		if _, ok := s.GetCtx(context.Background(), "a"); !ok {
			t.Error("Get during flush walk missed a key that is still on disk")
		}
	}

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if hookRuns == 0 {
		t.Fatal("walk hook never ran; flush did not visit any files")
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, ok := s.Get(key); ok {
			t.Errorf("key %s survived flush", key)
		}
	}
}

// TestFileStore_CleanupRecheckSparesRefreshedEntry exercises the lock-free
// walk's lost-update guard: a path observed as expired during the walk must
// be re-verified under the lock before removal, so an entry refreshed by a
// concurrent Put in between is spared.
func TestFileStore_CleanupRecheckSparesRefreshedEntry(t *testing.T) {
	s, err := NewFileStore("recheck", t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	writeExpiredFile(t, s, "key", "old")
	path := s.getCacheFilePath("key")

	// Simulate: walk collected the path as expired, then a Put refreshed
	// the entry before the removal phase reached it.
	if err := s.Put("key", "new", time.Hour); err != nil {
		t.Fatalf("Put refresh: %v", err)
	}
	s.removeIfEligible(path)

	if v, ok := s.Get("key"); !ok || v != "new" {
		t.Errorf("refreshed entry after re-check = (%v, %v), want (new, true)", v, ok)
	}

	// Control: without the refresh, the same candidate is removed.
	writeExpiredFile(t, s, "gone", "old")
	gonePath := s.getCacheFilePath("gone")
	s.removeIfEligible(gonePath)
	if _, err := os.Stat(gonePath); !os.IsNotExist(err) {
		t.Error("expired, un-refreshed candidate survived re-check removal")
	}
}
