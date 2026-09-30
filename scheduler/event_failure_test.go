package scheduler

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
func TestScheduler_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	s := New()
	s.SetEventDispatcher(func(context.Context, interface{}) error { return errors.New("listener failed") })
	for i := 0; i < 2; i++ {
		dispatchScheduledTaskFinished(&s.events, context.Background(), "t", 0)
	}
	if n := strings.Count(out.String(), "WARN event dispatch failed"); n != 1 || !strings.Contains(out.String(), "event=scheduler.task.completed") {
		t.Errorf("fallback output = %q, want one warn line naming scheduler.task.completed", out.String())
	}
}
