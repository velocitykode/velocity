package interceptors

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
func TestCallLifecycle_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	unary := CallLifecycle(WithRequestLine(), WithEventDispatcher(func(context.Context, any) error { return errors.New("listener failed") })).Unary
	info := &grpc.UnaryServerInfo{FullMethod: "/svc.Test/Call"}
	for i := 0; i < 2; i++ {
		if _, err := unary(context.Background(), nil, info, func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
			t.Fatalf("call: %v", err)
		}
	}
	got := out.String()
	if n := strings.Count(got, "WARN event dispatch failed"); n != 2 {
		t.Errorf("warn lines = %d, want 2 (one per event name): %q", n, got)
	}
	for _, name := range []string{"event=grpc.request.started", "event=grpc.request.completed"} {
		if !strings.Contains(got, name) {
			t.Errorf("fallback output = %q, want a warn line naming %s", got, name)
		}
	}
}
