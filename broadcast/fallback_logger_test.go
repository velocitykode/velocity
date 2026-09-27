package broadcast

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A manager without a logger writes its configuration warning through the
// one framework fallback, and nothing through the standard library log
// package or slog.Default.
func TestBroadcastManager_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	(&BroadcastManager{}).SetAuthorizer(func(string, interface{}) bool { return true })

	if got := fallback.Count("WARN", "broadcast: custom authorizer installed without an auth secret"); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}
