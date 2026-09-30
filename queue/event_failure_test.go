package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

func failingDispatch(context.Context, interface{}) error { return errors.New("listener failed") }

// assertOneWarn checks out holds exactly one failure warn line, naming
// event.
func assertOneWarn(t *testing.T, got, event string) {
	t.Helper()
	if n := strings.Count(got, "WARN event dispatch failed"); n != 1 || !strings.Contains(got, "event="+event) {
		t.Errorf("fallback output = %q, want one warn line naming %s", got, event)
	}
}

// A failed event dispatch goes to the one failure policy: counted, and the
// first failure of each event name logged at warn level through the
// component's logger (the standalone fallback here), not ignored.
func TestDriverCore_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var c DriverCore
	c.SetEventDispatcher(failingDispatch)
	for i := 0; i < 2; i++ {
		c.DispatchEvent(context.Background(), &JobQueued{})
	}
	assertOneWarn(t, out.String(), "queue.job.queued")
}

func TestWorker_FailedEventDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	w := NewWorker(NewMemoryDriver(), "default", func(Job) error { return nil })
	w.SetEventDispatcher(failingDispatch)
	for i := 0; i < 2; i++ {
		w.dispatchEvent(context.Background(), &JobProcessed{})
	}
	assertOneWarn(t, out.String(), "queue.job.completed")
}

func TestBatchEvents_FailedGlobalDispatchLoggedOncePerEvent(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	SetGlobalEventDispatcher(failingDispatch, nil, nil)
	// The emitter is process-wide, and so is the record of which event
	// names already logged: record into a fresh one, so a rerun (-count)
	// logs its first failure again.
	globalBatchEvents.Share(&eventemit.Failures{})
	t.Cleanup(func() {
		SetGlobalEventDispatcher(nil, nil, nil)
		globalBatchEvents.Share(nil)
	})
	for i := 0; i < 2; i++ {
		dispatchBatchEvent(context.Background(), nil, func(meta contract.EventMeta) contract.Event {
			return &BatchCreated{EventMeta: meta}
		})
	}
	assertOneWarn(t, out.String(), "queue.batch.created")
}
