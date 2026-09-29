package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// afterCommitFailing waits for the commit and then fails.
type afterCommitFailing struct{ failingListener }

func (afterCommitFailing) ShouldDispatchAfterCommit() bool { return true }

// afterCommitTally waits for the commit and then counts the event.
type afterCommitTally struct{ tallyListener }

func (afterCommitTally) ShouldDispatchAfterCommit() bool { return true }

// The after-commit part of a dispatch is one delivery: however many of its
// listeners fail, it is recorded once, with their failures joined, and the
// commit returns them; a rolled-back transaction records nothing. Every
// dispatcher that defers listeners to the commit behaves the same way.
func TestAfterCommitDelivery_RecordedOncePerDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func() (Dispatcher, *DefaultDispatcher)
	}{
		{"Default", func() (Dispatcher, *DefaultDispatcher) { d := NewDispatcher(); return d, d }},
		{"QueueIntegrated", func() (Dispatcher, *DefaultDispatcher) {
			d := NewQueueIntegratedDispatcher()
			return d, d.DefaultDispatcher
		}},
		{"Stoppable", func() (Dispatcher, *DefaultDispatcher) {
			d := NewStoppablePropagationDispatcher()
			return d, d.DefaultDispatcher
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, base := tc.make()
			var recorded atomic.Int32
			var recordedErr error
			base.SetDetachedFailureRecorder(func(_ context.Context, err error, _ any) {
				recorded.Add(1)
				recordedErr = err
			})
			var seen atomic.Int32
			d.Listen("evt", afterCommitFailing{})
			d.Listen("evt", afterCommitTally{tallyListener{n: &seen}})
			d.Listen("evt", afterCommitFailing{})

			ctx, _ := InstallAfterCommitQueue(context.Background())
			if err := d.Dispatch(ctx, "evt"); err != nil {
				t.Fatalf("Dispatch = %v, want nil before the commit", err)
			}
			if got := PendingAfterCommit(ctx); got != 1 {
				t.Errorf("pending after-commit tasks = %d, want 1 per dispatch", got)
			}
			err := FireAfterCommit(ctx)
			if !errors.Is(err, errListenerBroke) {
				t.Errorf("FireAfterCommit = %v, want the listeners' failure", err)
			}
			if recorded.Load() != 1 || !errors.Is(recordedErr, errListenerBroke) {
				t.Errorf("recorded %d times (%v), want once with the failures", recorded.Load(), recordedErr)
			}
			if seen.Load() != 1 {
				t.Errorf("healthy after-commit listener ran %d times, want 1", seen.Load())
			}

			before := recorded.Load()
			rolledBack, _ := InstallAfterCommitQueue(context.Background())
			_ = d.Dispatch(rolledBack, "evt")
			DropAfterCommit(rolledBack)
			if recorded.Load() != before {
				t.Errorf("a rolled-back transaction was recorded")
			}
		})
	}
}
