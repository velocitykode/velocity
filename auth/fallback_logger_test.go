package auth

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// An auth manager and a bcrypt hasher without a logger write their
// warnings through the one framework fallback, and nothing through the
// standard library log package or slog.Default.
func TestManagerAndHasher_WithoutLoggerWarnThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	NewManager().logWarn("velocity/auth: login redirect refused", "error", "external host")
	NewBcryptHasher(minSecureBcryptCost).SetCost(minSecureBcryptCost - 1)

	if got := fallback.Count("WARN", "velocity/auth: login redirect refused"); got != 1 {
		t.Errorf("manager lines = %d, want 1: %q", got, fallback.String())
	}
	if got := fallback.Count("WARN", "auth: bcrypt cost below secure minimum, clamped"); got != 1 {
		t.Errorf("hasher lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}
