package bus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

type failureProbeCommand struct{}

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
// The command still runs and succeeds.
func TestBus_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	b := New()
	ran := 0
	Register[failureProbeCommand](b, func(failureProbeCommand) error { ran++; return nil })
	b.SetEventDispatcher(func(context.Context, any) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		if err := b.Dispatch(failureProbeCommand{}); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
	}
	if ran != 2 {
		t.Fatalf("handler ran %d times, want 2", ran)
	}
	got := out.String()
	for _, name := range []string{"bus.command.started", "bus.command.completed"} {
		if !strings.Contains(got, "event="+name) {
			t.Errorf("fallback output = %q, want a warn line naming %s", got, name)
		}
	}
	if n := strings.Count(got, "WARN event dispatch failed"); n != 2 {
		t.Errorf("warn lines = %d, want 2 (one per event name): %q", n, got)
	}
}
