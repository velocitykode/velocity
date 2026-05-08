package scheduler

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// TestJob_PanicDispatchesFailed covers Task 7c: when a job callback panics,
// ScheduledTaskFailed is dispatched eagerly inside the recover block, before
// Run() returns, and is not dispatched a second time from the trailing
// error-handling path.
func TestJob_PanicDispatchesFailed(t *testing.T) {
	s := New()

	var (
		mu     sync.Mutex
		events []interface{}
	)
	s.SetEventDispatcher(func(_ context.Context, e interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		return nil
	})

	job := s.Call(func() { panic("kaboom") }).Name("panicking-job")

	_ = job.Run()

	mu.Lock()
	defer mu.Unlock()

	// Expect exactly one ScheduledTaskStarting + one ScheduledTaskFailed.
	var failed []*ScheduledTaskFailed
	for _, e := range events {
		if f, ok := e.(*ScheduledTaskFailed); ok {
			failed = append(failed, f)
		}
	}
	if len(failed) != 1 {
		t.Fatalf("expected 1 ScheduledTaskFailed, got %d (all events: %v)", len(failed), events)
	}
	if failed[0].TaskName != "panicking-job" {
		t.Errorf("TaskName = %q, want panicking-job", failed[0].TaskName)
	}
	if !strings.Contains(failed[0].Error, "kaboom") {
		t.Errorf("Error = %q, want substring 'kaboom'", failed[0].Error)
	}
}
