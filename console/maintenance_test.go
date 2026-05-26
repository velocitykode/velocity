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
