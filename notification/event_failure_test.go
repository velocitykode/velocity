package notification

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
// Nothing reaches the standard library log.
func TestManager_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	std := fallbacklogtest.CaptureStdlib(t)
	m := NewManager()
	m.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		m.events.EmitBuilt(context.Background(), func() any { return &NotificationSent{} })
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event=notification.completed") {
		t.Errorf("fallback output = %q, want one warn line naming notification.completed", out.String())
	}
	if s := std.String(); s != "" {
		t.Errorf("standard library log got %q, want nothing", s)
	}
}
