package velocity

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/queue"
)

// panickingBatch dispatches a one-job batch on a whose own dispatcher
// panics on every event, so its queue.batch.created delivery fails in the
// batch emitter rather than in a listener of the app's dispatcher.
func panickingBatch(t *testing.T, a *App) {
	t.Helper()
	_, err := queue.NewBatch(&processedJob{ID: "batch"}).
		WithEventDispatcher(func(context.Context, interface{}) { panic("batch dispatcher broke") }).
		Dispatch(context.Background(), a.Queue)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
}

// A batch's own dispatcher that panics is a failed event dispatch like any
// other: it counts once in the app's one failure counter and its first
// failure is logged through the app logger, so an operator watching
// FailedEventCount sees batch listener failures.
func TestBatchEventFailures_CountInTheAppCounter(t *testing.T) {
	a, logger := newLoggerWiringApp(t, nil)
	before := a.FailedEventCount()

	panickingBatch(t, a)

	if got := a.FailedEventCount() - before; got != 1 {
		t.Errorf("FailedEventCount grew by %d, want 1 (the batch dispatcher's panic)", got)
	}
	if got := eventFailureWarns(logger, "queue.batch.created"); got != 1 {
		t.Errorf("app logger has %d warn lines for queue.batch.created, want 1", got)
	}
}

// The batch emitter is process-wide: the newest app owns it, and a
// failure counts in the owner's counter alone.
func TestBatchEventFailures_CountInTheOwningApp(t *testing.T) {
	older, _ := newLoggerWiringApp(t, nil)
	newer, _ := newLoggerWiringApp(t, nil)
	olderBefore, newerBefore := older.FailedEventCount(), newer.FailedEventCount()

	panickingBatch(t, newer)

	if got := newer.FailedEventCount() - newerBefore; got != 1 {
		t.Errorf("owner's FailedEventCount grew by %d, want 1", got)
	}
	if got := older.FailedEventCount() - olderBefore; got != 0 {
		t.Errorf("older app's FailedEventCount grew by %d, want 0", got)
	}
}
