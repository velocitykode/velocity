package bus

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/trace"
)

// TestBus_Events_CarryTheEnvelope asserts the command events carry the
// dispatching context's trace ids and the handler's run time, and that a
// failed command's event holds the handler's own error.
func TestBus_Events_CarryTheEnvelope(t *testing.T) {
	b := New()
	var got []any
	b.SetEventDispatcher(func(_ context.Context, event any) error {
		got = append(got, event)
		return nil
	})
	boom := errors.New("oops")
	Register(b, func(createUser) error { return boom })
	_ = b.Dispatch(createUser{})

	if len(got) != 2 {
		t.Fatalf("events = %v, want started and failed", got)
	}
	started, ok := got[0].(*CommandDispatching)
	if !ok || started.At.IsZero() || started.Context == nil {
		t.Fatalf("started = %#v, want a stamped envelope", got[0])
	}
	failed, ok := got[1].(*CommandFailed)
	if !ok {
		t.Fatalf("second event = %T, want *CommandFailed", got[1])
	}
	if failed.Err != boom {
		t.Errorf("CommandFailed.Err = %v, want the handler's error", failed.Err)
	}
	if failed.Duration < 0 || failed.At.Before(started.At) {
		t.Errorf("CommandFailed Duration = %v, At = %v; want the run after %v", failed.Duration, failed.At, started.At)
	}

	ctx := trace.WithFullContext(context.Background(), "t1", "s1", "p1")
	got = nil
	b.SetQueue(&mockQueuePusher{})
	if err := b.DispatchAsyncCtx(ctx, createUser{}); err != nil {
		t.Fatalf("DispatchAsyncCtx: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %v, want one CommandQueued", got)
	}
	q, ok := got[0].(*CommandQueued)
	if !ok || q.TraceID != "t1" || q.SpanID != "s1" || q.ParentID != "p1" {
		t.Errorf("queued event = %#v, want CommandQueued with the caller's ids t1/s1/p1", got[0])
	}
}

// TestCommandFailed_JSONForm asserts the failed event's JSON form carries
// Err as its text and decodes back to an error with that text.
func TestCommandFailed_JSONForm(t *testing.T) {
	data, err := json.Marshal(&CommandFailed{CommandType: "bus.createUser", Err: errors.New("oops")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if fields["Err"] != "oops" {
		t.Errorf("JSON Err = %v, want %q (%s)", fields["Err"], "oops", data)
	}
	var back CommandFailed
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if back.Err == nil || back.Err.Error() != "oops" || back.CommandType != "bus.createUser" {
		t.Errorf("decoded = %+v, want Err oops and the command type", back)
	}
	if data, err := json.Marshal(CommandFailed{}); err != nil || string(data) == "" {
		t.Errorf("marshal of an event without Err: %s, %v", data, err)
	}
}

// TestCommandJob_FailedCarriesTheAttemptsTrace asserts the CommandFailed a
// queued command dispatches when its retries run out carries the trace ids
// of the attempt that failed it, which the worker hands its Failed hook.
func TestCommandJob_FailedCarriesTheAttemptsTrace(t *testing.T) {
	b := New()
	var failed []*CommandFailed
	b.SetEventDispatcher(func(_ context.Context, event any) error {
		if e, ok := event.(*CommandFailed); ok {
			failed = append(failed, e)
		}
		return nil
	})
	job := &commandJob{bus: b, Type: "bus.createUser"}
	ctx := trace.WithFullContext(context.Background(), "t1", "s1", "p1")
	boom := errors.New("retries exhausted")
	if err := queue.RunFailedHook(ctx, job, boom); err != nil {
		t.Fatalf("RunFailedHook: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("CommandFailed dispatched %d times, want 1", len(failed))
	}
	if got := failed[0]; got.TraceID != "t1" || got.SpanID != "s1" || got.ParentID != "p1" || got.Err != boom {
		t.Errorf("CommandFailed = {ids %s/%s/%s, Err %v}, want the attempt's t1/s1/p1 and its error", got.TraceID, got.SpanID, got.ParentID, got.Err)
	}
}
