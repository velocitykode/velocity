package schemes

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A session scheme without a logger writes its warnings through the one
// framework fallback.
func TestSessionScheme_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)

	(&SessionScheme{}).logWarn("velocity/auth: session not saved", "error", "store down")

	if got := fallback.Count("WARN", "velocity/auth: session not saved"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
}
