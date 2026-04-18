package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalDriver_RejectsTraversal covers the traversal branch of
// normalizeRelative. os.Root would reject the open too, but catching
// ".." lexically produces a clearer error for callers and keeps the
// behaviour stable across platforms.
func TestLocalDriver_RejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	// Traversal via Put.
	if err := d.Put("../outside.txt", []byte("evil")); err == nil {
		t.Fatal("Put should reject traversal path")
	}

	// Traversal via Get.
	if _, err := d.Get("../outside.txt"); err == nil {
		t.Fatal("Get should reject traversal path")
	}

	// The "outside" file should not have been created on disk.
	parent := filepath.Dir(dir)
	if _, err := os.Stat(filepath.Join(parent, "outside.txt")); err == nil {
		t.Error("traversal leaked a file outside the root")
	}
}

// TestLocalDriver_RejectsSymlinkEscape verifies that an in-root symlink
// whose target lives outside root is refused by os.Root's kernel-level
// containment (openat2 RESOLVE_NO_SYMLINKS on Linux; equivalent
// enforcement on other platforms).
func TestLocalDriver_RejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	// Out-of-root target that the attacker wants to read.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.lnk")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	if data, err := d.Get("escape.lnk"); err == nil {
		t.Fatalf("Get should refuse symlink escape, got data=%q", data)
	}
	if _, err := d.GetStream("escape.lnk"); err == nil {
		t.Fatal("GetStream should refuse symlink escape")
	}
}

// TestLocalDriver_Shutdown_ClosesRootOnce verifies Shutdown is
// idempotent (second call is a no-op) and releases the root FD.
func TestLocalDriver_Shutdown_ClosesRootOnce(t *testing.T) {
	dir := t.TempDir()
	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})

	if d.rootHandle == nil {
		t.Fatal("NewLocalDriver should open an os.Root")
	}
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if d.rootHandle != nil {
		t.Error("Shutdown should nil out the root handle")
	}
	// Second call is idempotent.
	if err := d.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}

	// Operations after shutdown surface ErrInvalidPath rather than
	// panicking on a closed FD.
	err := d.Put("anything.txt", []byte("x"))
	if err == nil {
		t.Fatal("Put after shutdown should fail")
	}
	if !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}

// TestManager_Shutdown_DrainsLocalDrivers verifies the manager walks
// each disk's Shutdown method, so the *os.Root FD is released during
// app shutdown.
func TestManager_Shutdown_DrainsLocalDrivers(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Default: "disk1",
		Disks: map[string]DiskConfig{
			"disk1": {Driver: "local", Root: dir},
		},
	}
	m := NewManager(cfg)
	if err := m.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	drv, err := m.Disk("disk1")
	if err != nil {
		t.Fatal(err)
	}
	local, ok := drv.(*LocalDriver)
	if !ok {
		t.Fatalf("expected *LocalDriver, got %T", drv)
	}
	if local.rootHandle == nil {
		t.Fatal("local driver should hold an open root")
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if local.rootHandle != nil {
		t.Error("manager.Shutdown should have closed the disk's root")
	}
}

// TestLocalDriver_AbsolutePathRejected guards the regression where a
// caller passing an absolute string bypassed the old prefix check.
func TestLocalDriver_AbsolutePathRejected(t *testing.T) {
	dir := t.TempDir()
	d := NewLocalDriver(DiskConfig{Driver: "local", Root: dir})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

	err := d.Put("/etc/passwd", []byte("malicious"))
	if err == nil {
		t.Fatal("Put with absolute path should be refused")
	}
	if !strings.Contains(err.Error(), "absolute") && !errors.Is(err, ErrInvalidPath) {
		t.Errorf("want ErrInvalidPath, got %v", err)
	}
}
