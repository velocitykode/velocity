package queue

import (
	"context"
	"testing"
)

// TestDriverCore_DispatchBuilt requires the builder to run only while a
// dispatcher is installed, and the event it returns to reach the
// dispatcher.
func TestDriverCore_DispatchBuilt(t *testing.T) {
	var c DriverCore
	built := 0
	build := func() any { built++; return &JobFailed{JobType: "unknown"} }
	if c.DispatchBuilt(context.Background(), build) || built != 0 {
		t.Fatalf("with no dispatcher: installed reported, or the builder ran %d times", built)
	}
	var got []any
	c.SetEventDispatcher(func(_ context.Context, event any) error {
		got = append(got, event)
		return nil
	})
	if !c.DispatchBuilt(context.Background(), build) || built != 1 {
		t.Fatalf("with a dispatcher: not reported installed, or the builder ran %d times", built)
	}
	if len(got) != 1 || got[0].(*JobFailed).JobType != "unknown" {
		t.Errorf("dispatcher received %v, want the built JobFailed", got)
	}
}
