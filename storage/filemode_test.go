package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// statMode reads the mode bits of a file inside the driver's root.
// Tests skip on Windows where Unix permission bits do not apply.
func statMode(t *testing.T, root, rel string) os.FileMode {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are POSIX-only")
	}
	info, err := os.Stat(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	return info.Mode().Perm()
}

// TestLocalDriver_PutStream_DefaultsTo0o600 pins FS-MODE-PUTSTREAM for
// the streaming write path. The umask-derived 0o644 default leaked
// every uploaded body to local-user readers; PutStream must Chmod
// before the data lands.
func TestLocalDriver_PutStream_DefaultsTo0o600(t *testing.T) {
	dir := t.TempDir()
	// Force a permissive umask so the bug would surface if the fix
	// regressed; we still want 0o600 on disk afterward.
	old := setUmask(0o022)
	defer setUmask(old)

	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	if err := d.PutStream("upload.bin", bytes.NewReader([]byte("payload"))); err != nil {
		t.Fatalf("PutStream: %v", err)
	}
	if got := statMode(t, dir, "upload.bin"); got != 0o600 {
		t.Errorf("PutStream wrote mode %o, want 0o600", got)
	}
}

// TestLocalDriver_Copy_DefaultsTo0o600 pins FS-MODE-PUTSTREAM for Copy.
func TestLocalDriver_Copy_DefaultsTo0o600(t *testing.T) {
	dir := t.TempDir()
	old := setUmask(0o022)
	defer setUmask(old)

	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	if err := d.Put("source.bin", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := d.Copy("source.bin", "dest.bin"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if got := statMode(t, dir, "dest.bin"); got != 0o600 {
		t.Errorf("Copy wrote mode %o, want 0o600", got)
	}
}

// TestLocalDriver_Put_StaysAt0o600 is a regression guard for the
// already-correct Put path; the FS-MODE fix must not perturb it.
func TestLocalDriver_Put_StaysAt0o600(t *testing.T) {
	dir := t.TempDir()
	old := setUmask(0o022)
	defer setUmask(old)

	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	if err := d.Put("file.bin", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := statMode(t, dir, "file.bin"); got != 0o600 {
		t.Errorf("Put wrote mode %o, want 0o600", got)
	}
}
