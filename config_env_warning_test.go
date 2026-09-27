package velocity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// ConfigFromEnv runs before any app logger exists: a .env file it cannot
// parse is reported once through the framework's fallback logger, and
// nothing goes through the standard library log.
func TestConfigFromEnv_UnparsableEnvFileWarnsThroughTheFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("BROKEN=\"unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	stdlib := fallbacklogtest.CaptureStdlib(t)
	fallback := fallbacklogtest.Capture(t)

	_ = ConfigFromEnv()

	if got := fallback.Count("WARN", "velocity: .env file exists but failed to parse"); got != 1 {
		t.Errorf("fallback warn lines = %d, want 1 (%q)", got, fallback.String())
	}
	if s := stdlib.String(); s != "" {
		t.Errorf("stdlib got %q, want nothing", s)
	}
}
