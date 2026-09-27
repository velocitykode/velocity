package scheduler

import (
	"context"
	"testing"
)

// TestScheduler_NoDispatcherBuildsNoEvent requires a run to build no task
// events when no event dispatcher is installed: it allocates less than the
// same run with a dispatcher that discards every event.
func TestScheduler_NoDispatcherBuildsNoEvent(t *testing.T) {
	s := New()
	j := s.Call(func() {})
	run := func() { _ = j.Run() }

	without := testing.AllocsPerRun(100, run)
	s.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
	with := testing.AllocsPerRun(100, run)
	if without >= with {
		t.Errorf("run allocated %.0f times with no dispatcher and %.0f with one, want fewer without", without, with)
	}
}
