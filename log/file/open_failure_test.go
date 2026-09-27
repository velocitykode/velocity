package file

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A file driver that cannot open its log file writes one error line per
// dropped record through the framework's fallback logger (standard error),
// and nothing to standard output.
func TestFileLogger_OpenFailureWritesThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	// A regular file where the log directory should be: MkdirAll fails.
	notADir := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := NewFileLogger(notADir, 0, 0)
	defer logger.Shutdown(context.Background())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	logger.Error("dropped record")
	os.Stdout = orig
	_ = w.Close()
	var stdout bytes.Buffer
	_, _ = stdout.ReadFrom(r)

	if got := fallback.Count("ERROR", "velocity/log: open log file failed"); got != 1 {
		t.Errorf("fallback error lines = %d, want 1 (%q)", got, fallback.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", stdout.String())
	}
}
