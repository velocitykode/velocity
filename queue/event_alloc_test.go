package queue

import (
	"context"
	"testing"
	"time"
)

// TestMemoryDriver_NoDispatcherBuildsNoEvent requires a push to build no
// JobQueued event when no event dispatcher is installed: it allocates less
// than the same push with a dispatcher that discards every event.
func TestMemoryDriver_NoDispatcherBuildsNoEvent(t *testing.T) {
	ctx := context.Background()
	measure := func(m *MemoryDriver) float64 {
		return testing.AllocsPerRun(200, func() { _ = m.PushCtx(ctx, &TestJob{ID: "a"}) })
	}
	without := measure(NewMemoryDriver())
	m := NewMemoryDriver()
	m.SetEventDispatcher(func(context.Context, interface{}) error { return nil })
	with := measure(m)
	if without >= with {
		t.Errorf("push allocated %.0f times with no dispatcher and %.0f with one, want fewer without", without, with)
	}
}

// TestWorker_NoDispatcherBuildsNoEvent requires the job events a worker
// dispatches to build nothing when no event dispatcher is installed.
func TestWorker_NoDispatcherBuildsNoEvent(t *testing.T) {
	w := NewWorker(NewMemoryDriver(), "default", func(Job) error { return nil })
	ctx := context.Background()
	allocs := testing.AllocsPerRun(100, func() {
		dispatchJobProcessing(w.jobEventDispatch(), ctx, "t", "default")
		dispatchJobProcessed(w.jobEventDispatch(), ctx, "t", "default", time.Millisecond)
		dispatchJobRetrying(w.jobEventDispatch(), ctx, "t", "default", 1, 3, nil, time.Second)
	})
	if allocs != 0 {
		t.Errorf("job event helpers allocated %.0f times with no dispatcher, want 0", allocs)
	}
}
