package async

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// A function that panics in RaceWithTimeout is reported through the panic
// hook like a panic in every other helper, and the race still returns the
// first value.
func TestRaceWithTimeout_ReportsAPanic(t *testing.T) {
	var hooked atomic.Int32
	SetPanicHook(func(context.Context, any) { hooked.Add(1) })
	t.Cleanup(func() { SetPanicHook(nil) })

	racer := hostile.New(t, hostile.Panic, nil)
	release := make(chan struct{})
	r := RaceWithTimeout(time.Second,
		func() int { racer.Run(); return 0 },
		func() int { <-release; return 7 },
	)
	deadline := time.Now().Add(2 * time.Second)
	for hooked.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	if v, err := r.Get(); err != nil || v != 7 {
		t.Errorf("Get = %v, %v, want 7, nil", v, err)
	}
	if got := hooked.Load(); got != 1 {
		t.Errorf("panic hook calls = %d, want 1", got)
	}
}
