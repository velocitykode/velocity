package drivers

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFileStore_CleanupPurgesStaleUnreadableFiles proves the background
// cleanup removes unreadable files (corrupt, or legacy-format from before the
// value-encoding change) once they are older than the grace window, while
// leaving freshly-modified unreadable files alone -- those may be an in-flight
// write by another instance/process sharing the path. Guards both the
// leak-fix and the regression of deleting live concurrent writes.
func TestFileStore_CleanupPurgesStaleUnreadableFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileStoreWithOptions("purge", dir, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewFileStoreWithOptions: %v", err)
	}
	s.Start()
	defer func() { _ = s.Shutdown(context.Background()) }()

	// Fresh unreadable file (simulates an in-flight write by another instance):
	// recent mtime, must SURVIVE cleanup.
	fresh := filepath.Join(dir, "fresh.cache")
	if err := os.WriteFile(fresh, []byte("partial-write-not-json"), 0o600); err != nil {
		t.Fatalf("write fresh file: %v", err)
	}

	// Stale unreadable files (legacy + garbage): backdate mtime past the grace
	// window so cleanup PURGES them.
	stale := map[string][]byte{
		filepath.Join(dir, "legacy.cache"):  []byte(`{"value":"v","expiration":null}`),
		filepath.Join(dir, "garbage.cache"): []byte("not json at all"),
	}
	old := time.Now().Add(-2 * fileUnreadableGrace)
	for path, data := range stale {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write stale file %s: %v", path, err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("backdate %s: %v", path, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		allStaleGone := true
		for path := range stale {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				allStaleGone = false
			}
		}
		if allStaleGone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not purge stale unreadable files within deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The fresh file must still be present: cleanup ran (it purged the stale
	// ones) but must not delete a recently-modified unreadable file.
	if _, err := os.Stat(fresh); os.IsNotExist(err) {
		t.Fatal("cleanup deleted a fresh unreadable file (would clobber a live concurrent write)")
	}
}
