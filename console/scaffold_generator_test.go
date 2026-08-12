package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteScaffoldedFile_HardFailsOnMissingStub asserts the shared writer
// hard-fails when the requested stub is absent from the embedded FS instead
// of silently substituting an inline fallback. A bad stub path is a
// programmer error (a broken go:embed pattern), so it must surface loudly.
func TestWriteScaffoldedFile_HardFailsOnMissingStub(t *testing.T) {
	t.Chdir(t.TempDir())

	err := writeScaffoldedFile("Thing", "", "internal/things", "thing",
		"thing.go", "internal/things/does-not-exist.go.stub",
		map[string]any{"Name": "Thing"})
	if err == nil {
		t.Fatal("writeScaffoldedFile with a missing stub returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), "failed to read stub") {
		t.Errorf("error = %q, want it to mention %q", err.Error(), "failed to read stub")
	}
	// Nothing should have been written when the stub read fails.
	if _, statErr := os.Stat(filepath.Join("internal/things", "thing.go")); statErr == nil {
		t.Error("a file was written despite the stub read failing")
	}
}

// TestGenHandler_NestedRoutesThroughGenerator confirms the handler scaffolder
// still produces the name-derived nested directory and parent-derived package
// now that the write goes through the shared generator. The defense-in-depth
// EnsureWithinRoot / EnsureNoSymlinkComponents guards run before the generator,
// so a traversal name is rejected with nothing written outside the root.
func TestGenHandler_NestedRoutesThroughGenerator(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		t.Chdir(t.TempDir())

		if err := GenHandler("Admin/Reports", GenHandlerOptions{}); err != nil {
			t.Fatalf("GenHandler(Admin/Reports) error = %v", err)
		}

		path := filepath.Join("internal/handlers/admin", "reports.go")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected file at %s: %v", path, err)
		}
		// Package is derived from the parent segment, not the default "handlers".
		if !strings.Contains(string(content), "package admin") {
			t.Errorf("expected nested package admin in %s", path)
		}
	})

	t.Run("traversal-rejected", func(t *testing.T) {
		tmp := t.TempDir()
		t.Chdir(tmp)

		if err := GenHandler("../../tmp/owned", GenHandlerOptions{}); err == nil {
			t.Fatal("GenHandler accepted a traversal name, want error")
		}
		if _, statErr := os.Stat(filepath.Join(tmp, "..", "owned")); statErr == nil {
			t.Error("a file appeared outside the project root")
		}
	})
}
