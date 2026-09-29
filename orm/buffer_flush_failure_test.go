package orm

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// failingDomainDispatch fails every buffered domain event and passes every
// other (the transaction's own events).
func failingDomainDispatch(_ context.Context, e any) error {
	if _, ok := e.(*txDomainEvent); ok {
		return errors.New("listener failed")
	}
	return nil
}

// A buffered event whose flush fails reaches the app's failure policy once
// per failed delivery (the flush stops at the first), count and hook
// alike, and the transaction still returns the failure. A dispatch
// function that already recorded the failure is not counted again.
func TestBuffer_FailedFlushReachesTheFailurePolicyOnce(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recording bool
	}{
		{"raw dispatcher", false},
		{"recording dispatcher", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fallbacklogtest.Capture(t)
			m := newTestManager(t)
			t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
			shared := &eventemit.Failures{}
			var hooked atomic.Int32
			shared.SetHook(func(err error, event any) {
				if _, ok := event.(*txDomainEvent); ok {
					hooked.Add(1)
				}
			})
			m.ShareEventFailures(shared)
			dispatch := failingDomainDispatch
			if tc.recording {
				dispatch = shared.Recording(failingDomainDispatch, nil)
			}
			m.SetEventDispatcher(dispatch)

			ctx := events.PrepareBuffer(context.Background())
			err := m.Transaction(ctx, func(ctx context.Context) error {
				_ = events.Buffer(ctx).Dispatch(ctx, &txDomainEvent{Tag: "a"})
				_ = events.Buffer(ctx).Dispatch(ctx, &txDomainEvent{Tag: "b"}) // kept: the flush stops at the first failure
				return nil
			})
			if err == nil {
				t.Error("Transaction = nil, want the failed flush")
			}
			if got := shared.Count(); got != 1 {
				t.Errorf("failure count = %d, want 1 (one per failed delivery)", got)
			}
			if got := hooked.Load(); got != 1 {
				t.Errorf("hook calls = %d, want 1", got)
			}
		})
	}
}
