package websocket

import (
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A server without a logger (or with a nil one) writes its warnings and
// errors through the one framework fallback.
func TestServer_WithoutLoggerWritesThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Server)
	}{
		{"never set", func(*Server) {}},
		{"set to nil", func(s *Server) { s.SetLogger(nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			s := &Server{}
			tc.setup(s)

			s.logWarn("Client send channel full, skipping message", "client_id", "c1")
			s.logError("Failed to upgrade connection", "error", "bad handshake")

			if got := fallback.Count("WARN", "Client send channel full"); got != 1 {
				t.Errorf("warn lines = %d, want 1: %q", got, fallback.String())
			}
			if got := fallback.Count("ERROR", "Failed to upgrade connection"); got != 1 {
				t.Errorf("error lines = %d, want 1: %q", got, fallback.String())
			}
		})
	}
}
