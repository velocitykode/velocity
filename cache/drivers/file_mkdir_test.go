package drivers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// TestNewFileStore_CreatesRootOnce asserts the root cache directory is
// created at construction time rather than on first Put. This locks in the
// MkdirAll-once behaviour the driver now relies on.
func TestNewFileStore_CreatesRootOnce(t *testing.T) {
	tempDir := t.TempDir()
	root := filepath.Join(tempDir, "cache-root")

	// Sanity: the root doesn't exist yet.
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected %q to not exist before NewFileStore, stat err=%v", root, err)
	}

	store, err := NewFileStore("", root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()

	// Construction should have created the root.
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("expected root %q to exist after NewFileStore, stat err=%v", root, err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %q to be a directory", root)
	}
}

// TestFileStore_GetCacheFilePath_CachesShardDirs verifies that
// getCacheFilePath caches the 2-char shard directories so subsequent
// calls against the same shard don't re-issue MkdirAll. We can't directly
// observe the syscall, but we can observe the cached entry count in
// shardDirs and confirm that calls to the same shard don't add duplicates.
func TestFileStore_GetCacheFilePath_CachesShardDirs(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewFileStore("", tempDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()

	// Call twice with the same key — shardDirs must only hold one entry
	// for that shard.
	_ = store.getCacheFilePath("same-key")
	_ = store.getCacheFilePath("same-key")

	count := 0
	store.shardDirs.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Errorf("expected 1 cached shard after repeated same-key calls, got %d", count)
	}

	// A different key may or may not land in the same shard (depends on
	// SHA-256), but calling 500 random keys will touch many distinct
	// shards. Assert the cache grows monotonically without duplicates.
	for i := 0; i < 500; i++ {
		_ = store.getCacheFilePath(
			"load-test-" + filepath.Base(t.TempDir()) +
				"-" + time.Now().Format(time.RFC3339Nano) +
				"-" + string(rune('a'+i%26)),
		)
	}

	count = 0
	store.shardDirs.Range(func(_, _ any) bool {
		count++
		return true
	})
	// 256 possible shards (2 hex chars); after 500 inserts we expect more
	// than the single shard we started with but at most 256.
	if count < 2 {
		t.Errorf("expected >1 cached shards after many keys, got %d", count)
	}
	if count > 256 {
		t.Errorf("expected <=256 cached shards (2 hex chars), got %d", count)
	}
}

// TestFileStore_GetCacheFilePath_CreatesShardOnDisk verifies that the
// lazily-cached shard directory also exists on disk after first use so
// Put's os.WriteFile will succeed.
func TestFileStore_GetCacheFilePath_CreatesShardOnDisk(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewFileStore("", tempDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()

	path := store.getCacheFilePath("some-key")
	shardDir := filepath.Dir(path)

	info, err := os.Stat(shardDir)
	if err != nil {
		t.Fatalf("expected shard dir %q to exist, stat err=%v", shardDir, err)
	}
	if !info.IsDir() {
		t.Errorf("expected %q to be a directory", shardDir)
	}

	// And a real Put through the public API should succeed end-to-end.
	if err := store.Put("some-key", "value", time.Hour); err != nil {
		t.Errorf("Put after shard creation failed: %v", err)
	}
	if v, ok := store.Get("some-key"); !ok || v != "value" {
		t.Errorf("Get returned %v, %v; want 'value', true", v, ok)
	}
}

// TestNewFileStoreWithOptions_ConfigurableInterval asserts the constructor
// accepts a custom cleanup interval and falls back to the default when
// a non-positive value is supplied.
func TestNewFileStoreWithOptions_ConfigurableInterval(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"positive custom interval", 250 * time.Millisecond, 250 * time.Millisecond},
		{"zero falls back to default", 0, DefaultFileCleanupInterval},
		{"negative falls back to default", -time.Second, DefaultFileCleanupInterval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()
			store, err := NewFileStoreWithOptions("", tempDir, tc.in)
			if err != nil {
				t.Fatalf("NewFileStoreWithOptions: %v", err)
			}
			defer store.Close()
			if store.cleanupInterval != tc.want {
				t.Errorf("cleanupInterval = %v, want %v", store.cleanupInterval, tc.want)
			}
		})
	}
}

// TestFileStore_CleanupIntervalSweepsExpired is an end-to-end check that
// the configurable interval actually drives the sweep goroutine: with a
// short interval the expired file is removed without the test having to
// wait 5 minutes.
func TestFileStore_CleanupIntervalSweepsExpired(t *testing.T) {
	tempDir := t.TempDir()
	// 50ms interval so the sweep runs quickly.
	store, err := NewFileStoreWithOptions("", tempDir, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewFileStoreWithOptions: %v", err)
	}
	store.Start()
	defer store.Close()

	// Put an item that expires almost immediately.
	if err := store.Put("vanish", "value", 20*time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := store.getCacheFilePath("vanish")

	// The sweep loop runs on an interval; poll until the expired file is gone.
	testsync.Eventually(t, func() bool {
		_, err := os.Stat(path)
		return os.IsNotExist(err)
	}, 2*time.Second, "cleanup sweep removes expired file")
}

// TestFileStore_Shutdown asserts Shutdown(ctx) is wired and idempotent.
// The I-sweep landed Close(); Shutdown(ctx) is an additive hook and must
// not panic on repeat invocation.
func TestFileStore_Shutdown(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewFileStore("", tempDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	store.Start()

	// First shutdown stops the goroutine.
	if err := store.Shutdown(context.TODO()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// Second shutdown must be idempotent.
	if err := store.Shutdown(context.TODO()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
	// Close after Shutdown must also be idempotent.
	if err := store.Close(); err != nil {
		t.Errorf("Close after Shutdown: %v", err)
	}
}
