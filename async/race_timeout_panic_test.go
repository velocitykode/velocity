package async

import (
	"context"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A function that panics in RaceWithTimeout is reported through the panic
// hook like a panic in every other helper, and the race still returns the
// first value.
func TestRaceWithTimeout_ReportsAPanic(t *testing.T) {
	hooked := make(chan struct{}, 2)
	SetPanicHook(func(context.Context, any) { hooked <- struct{}{} })
	t.Cleanup(func() { SetPanicHook(nil) })

	racer := hostile.New(t, hostile.Panic, nil)
	release := make(chan struct{})
	// The race's own timeout is an hour, so a slow machine never ends it
	// before the value arrives.
	r := RaceWithTimeout(time.Hour,
		func() int { racer.Run(); return 0 },
		func() int { <-release; return 7 },
	)
	select {
	case <-hooked:
	case <-time.After(hostile.Deadline):
		t.Error("the panic never reached the panic hook")
	}
	close(release)
	if v, err := r.Get(); err != nil || v != 7 {
		t.Errorf("Get = %v, %v, want 7, nil", v, err)
	}
	if got := len(hooked); got != 0 {
		t.Errorf("panic hook called %d more times, want once", got)
	}
}
