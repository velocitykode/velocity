package interceptors

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// The call lifecycle interceptor built without a logger writes
// through the one framework fallback, and nothing through the standard
// library log package or slog.Default.
func TestInterceptors_WithoutLoggerWriteThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Test/Call"}

	_, _ = CallLifecycle().Unary(context.Background(), nil, info, func(context.Context, any) (any, error) {
		panic("handler blew up")
	})
	_, _ = CallLifecycle(WithRequestLine()).Unary(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, status.Error(codes.Internal, "store down")
	})

	if got := fallback.Count("ERROR", "gRPC panic recovered"); got != 1 {
		t.Errorf("recovery fallback lines = %d, want 1: %q", got, fallback.String())
	}
	if got := fallback.Count("ERROR", "gRPC request"); got != 1 {
		t.Errorf("logging fallback lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}
