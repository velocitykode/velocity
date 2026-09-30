package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
// A panicking dispatcher is contained and recorded the same way.
func TestServer_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	s := &Server{}
	s.SetEventDispatcher(func(context.Context, any) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		s.events.EmitBuilt(context.Background(), func() any { return &grpcevents.ServerStarted{} })
	}
	s.SetEventDispatcher(func(context.Context, any) error { panic("listener broke") })
	for i := 0; i < 2; i++ {
		s.events.EmitBuilt(context.Background(), func() any { return &grpcevents.ServerStopped{} })
	}
	got := out.String()
	if n := strings.Count(got, "WARN event dispatch failed"); n != 2 {
		t.Errorf("warn lines = %d, want 2 (one per event name): %q", n, got)
	}
	for _, name := range []string{"event=grpc.server.started", "event=grpc.server.stopped", "listener broke"} {
		if !strings.Contains(got, name) {
			t.Errorf("fallback output = %q, want it to hold %s", got, name)
		}
	}
}
