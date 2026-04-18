package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// TestLocalDriver_PermissionDenied verifies that the local driver surfaces
// filesystem permission errors instead of silently succeeding or panicking.
// Skipped on Windows (different permission model) and as root (chmod 0500
// still lets root write).
func TestLocalDriver_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission tests don't apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses filesystem permission checks")
	}

	root := t.TempDir()
	readOnly := filepath.Join(root, "readonly")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Restore write so t.TempDir cleanup can rm -rf.
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	driver := NewLocalDriver(DiskConfig{Driver: "local", Root: readOnly})

	t.Run("Put returns error", func(t *testing.T) {
		err := driver.Put("blocked.txt", []byte("nope"))
		if err == nil {
			t.Error("Put on read-only directory should error")
		}
	})

	t.Run("PutStream returns error", func(t *testing.T) {
		err := driver.PutStream("blocked.txt", bytes.NewReader([]byte("nope")))
		if err == nil {
			t.Error("PutStream on read-only directory should error")
		}
	})

	t.Run("MakeDirectory returns error", func(t *testing.T) {
		err := driver.MakeDirectory("new-dir")
		if err == nil {
			t.Error("MakeDirectory on read-only directory should error")
		}
	})
}

// TestLocalDriver_ConcurrentWrites exercises concurrent Put/Get/Delete on
// the same paths. Runs with -race; a driver that mutated shared state
// without a mutex would surface as a race report.
func TestLocalDriver_ConcurrentWrites(t *testing.T) {
	driver := NewLocalDriver(DiskConfig{Driver: "local", Root: t.TempDir()})

	const workers = 8
	const iters = 20
	var wg sync.WaitGroup
	var failures atomic.Int32

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			path := fmt.Sprintf("worker-%d.txt", id)
			for i := 0; i < iters; i++ {
				content := []byte(fmt.Sprintf("w=%d i=%d", id, i))
				if err := driver.Put(path, content); err != nil {
					failures.Add(1)
					t.Errorf("worker %d put %d: %v", id, i, err)
					return
				}
				got, err := driver.Get(path)
				if err != nil {
					failures.Add(1)
					t.Errorf("worker %d get %d: %v", id, i, err)
					return
				}
				if !bytes.Equal(got, content) {
					failures.Add(1)
					t.Errorf("worker %d: content mismatch after Put/Get round-trip: got %q want %q", id, got, content)
					return
				}
			}
			if err := driver.Delete(path); err != nil {
				failures.Add(1)
				t.Errorf("worker %d delete: %v", id, err)
			}
		}(w)
	}
	wg.Wait()

	if failures.Load() > 0 {
		t.Fatalf("%d concurrent operation(s) failed", failures.Load())
	}
}

// TestLocalDriver_DeleteIdempotent locks in the behaviour where Delete of a
// missing file does NOT error. Callers rely on this for cleanup loops.
func TestLocalDriver_DeleteIdempotent(t *testing.T) {
	driver := NewLocalDriver(DiskConfig{Driver: "local", Root: t.TempDir()})
	if err := driver.Delete("never-existed.txt"); err != nil {
		t.Errorf("Delete of missing file should not error, got %v", err)
	}
}
