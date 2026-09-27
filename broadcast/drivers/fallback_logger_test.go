package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A driver without a logger writes its warnings through the one framework
// fallback, and nothing through the standard library log package or
// slog.Default.
func TestWebSocketDriver_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	d := &WebSocketDriver{}
	d.recordDrop("client-1", "orders", "created")
	d.warnAuthorizerWithoutVerifier()

	if got := fallback.Count("WARN", "velocity/broadcast: dropped message"); got != 1 {
		t.Errorf("drop lines = %d, want 1: %q", got, fallback.String())
	}
	if got := fallback.Count("WARN", "velocity/broadcast: private/presence channels are gated only by the channel authorizer"); got != 1 {
		t.Errorf("verifier lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}
