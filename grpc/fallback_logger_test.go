package grpc

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A server and a gateway built without a logger (or with a nil one) write
// their warnings through the one framework fallback, and nothing through
// the standard library log package or slog.Default.
func TestServerAndGateway_WithoutLoggerWarnThroughTheFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		serverOpts []ServerOption
		gwOpts     []GatewayOption
	}{
		{"no option", nil, nil},
		{"nil logger", []ServerOption{WithLogger(nil)}, []GatewayOption{GatewayWithLogger(nil)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallback := fallbacklogtest.Capture(t)
			stdlib := fallbacklogtest.CaptureStdlib(t)

			s := NewServer(append([]ServerOption{WithPort("0"), WithEnvironment("development")}, tc.serverOpts...)...)
			if err := s.Build(); err != nil {
				t.Fatalf("server Build: %v", err)
			}
			t.Cleanup(s.Stop)
			g := NewGateway(append([]GatewayOption{GatewayWithEnvironment("development")}, tc.gwOpts...)...)
			if err := g.Build(context.Background()); err != nil {
				t.Fatalf("gateway Build: %v", err)
			}
			t.Cleanup(func() { _ = g.Shutdown(context.Background()) })

			if got := fallback.Count("WARN", "gRPC server starting without TLS credentials"); got != 1 {
				t.Errorf("server fallback lines = %d, want 1: %q", got, fallback.String())
			}
			if got := fallback.Count("WARN", "gRPC gateway dialling upstream with insecure credentials"); got != 1 {
				t.Errorf("gateway fallback lines = %d, want 1: %q", got, fallback.String())
			}
			if out := stdlib.String(); out != "" {
				t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
			}
		})
	}
}
