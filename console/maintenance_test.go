package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/velocitykode/velocity/internal/maintpath"
)

// useTempMaintRoot points the resolver at a fresh tmp dir and returns its
// absolute path. The cache is reset so the next Root() call re-reads the env.
func useTempMaintRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(maintpath.EnvVar, root)
	maintpath.Reset()
	t.Cleanup(maintpath.Reset)
	return root
}

func TestDown_CreatesMarkerFile(t *testing.T) {
	root := useTempMaintRoot(t)

	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	path := filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatalf("expected marker file at %s to exist", path)
	}
}

func TestDown_WritesJSONPayload(t *testing.T) {
	root := useTempMaintRoot(t)

	opts := DownOptions{
		Secret:     "bypass-secret",
		RetryAfter: 60,
	}
	if err := Down(opts); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile))
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}

	var payload downPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if payload.Secret != "bypass-secret" {
		t.Errorf("expected secret %q, got %q", "bypass-secret", payload.Secret)
	}
	if payload.RetryAfter != 60 {
		t.Errorf("expected retry_after %d, got %d", 60, payload.RetryAfter)
	}
	if payload.Time == "" {
		t.Error("expected time to be set")
	}
}

func TestDown_OmitsEmptyFields(t *testing.T) {
	root := useTempMaintRoot(t)

	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile))
	if err != nil {
		t.Fatalf("failed to read marker file: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}

	if _, exists := raw["secret"]; exists {
		t.Error("expected secret to be omitted when empty")
	}
	if _, exists := raw["retry_after"]; exists {
		t.Error("expected retry_after to be omitted when zero")
	}
}

func TestUp_RemovesMarkerFile(t *testing.T) {
	root := useTempMaintRoot(t)

	// Create the marker file first
	if err := Down(DownOptions{}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	if err := Up(); err != nil {
		t.Fatalf("Up() error = %v", err)
	}

	path := filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expected marker file to be removed")
	}
}

func TestUp_NoErrorWhenFileDoesNotExist(t *testing.T) {
	useTempMaintRoot(t)

	err := Up()
	if err != nil {
		t.Fatalf("Up() returned error when file does not exist: %v", err)
	}
}

// TestDown_FilePermissionsAreRestrictive asserts that the marker file is
// written with 0o600 because it may contain a bypass secret. This test is
// skipped on Windows where Unix-style permission bits are not enforced.
func TestDown_FilePermissionsAreRestrictive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on Windows")
	}
	root := useTempMaintRoot(t)

	if err := Down(DownOptions{Secret: "topsecret"}); err != nil {
		t.Fatalf("Down() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("marker file mode: got %o, want 0o600", mode)
	}

	dirInfo, err := os.Stat(filepath.Join(root, maintpath.MarkerDir))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("marker dir mode: got %o, want 0o700", mode)
	}
}

// TestDown_PermissionsSurviveRepeatedDown pins that calling Down twice in a
// row keeps the marker at 0o600 and the directory at 0o700. The default
// MkdirAll on a pre-existing directory does not chmod it, but the test
// is here so a future regression that adds an explicit looser chmod
// fails loudly. Similar story for the file: WriteFile on an existing
// path can preserve old perms in some runtimes.
func TestDown_PermissionsSurviveRepeatedDown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on Windows")
	}
	root := useTempMaintRoot(t)

	if err := Down(DownOptions{Secret: "first"}); err != nil {
		t.Fatalf("first Down: %v", err)
	}
	if err := Down(DownOptions{Secret: "second"}); err != nil {
		t.Fatalf("second Down: %v", err)
	}

	info, err := os.Stat(filepath.Join(root, maintpath.MarkerDir, maintpath.MarkerFile))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("marker file mode after repeat: got %o, want 0o600", mode)
	}

	dirInfo, err := os.Stat(filepath.Join(root, maintpath.MarkerDir))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("marker dir mode after repeat: got %o, want 0o700", mode)
	}
}

// TestDown_TightensPreExistingLooseMarker simulates a marker file left by
// an older Velocity release that wrote 0o644, then calls Down to verify
// the new code path actively chmods it down to 0o600 instead of leaving
// the loose perms in place (the pre-existing-file branch of os.WriteFile
// preserves perms, so an explicit chmod is required).
func TestDown_TightensPreExistingLooseMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on Windows")
	}
	root := useTempMaintRoot(t)

	// Simulate stale marker laid down by an older binary with loose perms.
	dir := filepath.Join(root, maintpath.MarkerDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	path := filepath.Join(dir, maintpath.MarkerFile)
	if err := os.WriteFile(path, []byte(`{"secret":"stale"}`), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	if err := Down(DownOptions{Secret: "new"}); err != nil {
		t.Fatalf("Down: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("marker file mode after re-down on loose existing file: got %o, want 0o600", mode)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("marker dir mode after re-down on loose existing dir: got %o, want 0o700", mode)
	}
}

// TestDown_NoOtherWorldReadablePaths walks the resolved marker dir after
// Down and asserts every entry under it has perms <=0o700 (dir) / 0o600
// (file). Defensive sweep for future code adding a new write site under
// .vel/ without thinking about perms.
func TestDown_NoOtherWorldReadablePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not enforced on Windows")
	}
	root := useTempMaintRoot(t)

	if err := Down(DownOptions{Secret: "x"}); err != nil {
		t.Fatalf("Down: %v", err)
	}

	dir := filepath.Join(root, maintpath.MarkerDir)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		// Other-bits (4) of any group must be zero. Group-bits also.
		if mode&0o077 != 0 {
			t.Errorf("path %q has group/other bits set: %o", path, mode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
