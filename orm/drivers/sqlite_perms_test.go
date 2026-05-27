//go:build cgo && unix

package drivers

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSQLiteDriver_DatabaseDirTightPerms verifies that Connect creates the
// SQLite database directory with mode 0o700. The directory holds the
// SQLite file containing every framework table (users, sessions, queue
// payloads, etc.) and must not default to world-readable.
func TestSQLiteDriver_DatabaseDirTightPerms(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(root, "subdir", "test.db")

	d := NewSQLiteDriver()
	if err := d.Connect(ConnectionConfig{Database: dbPath}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer d.Close()

	info, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("database directory mode = %#o, want 0700", got)
	}
}

// TestSQLiteDriver_DatabaseDirTightensPreExistingLooseMode verifies that
// a database directory laid down with 0o755 by an older binary is
// tightened to 0o700 on the next Connect call.
func TestSQLiteDriver_DatabaseDirTightensPreExistingLooseMode(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "subdir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed mkdir: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("seed chmod: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")

	d := NewSQLiteDriver()
	if err := d.Connect(ConnectionConfig{Database: dbPath}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer d.Close()

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("database directory mode = %#o, want 0700 (pre-existing loose mode must be tightened)", got)
	}
}
