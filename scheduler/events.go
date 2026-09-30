package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/trace"
)

// Compile-time assertions that every scheduler event implements
// contract.Event.
var (
	_ contract.Event = (*ScheduledTaskStarting)(nil)
	_ contract.Event = (*ScheduledTaskFinished)(nil)
	_ contract.Event = (*ScheduledTaskFailed)(nil)
)

// ScheduledTaskStarting is dispatched when a scheduled task begins
type ScheduledTaskStarting struct {
	contract.EventMeta
	TaskName string
}

// Name returns the event name
func (e *ScheduledTaskStarting) Name() string {
	return "scheduler.task.started"
}

// ScheduledTaskFinished is dispatched when a scheduled task completes successfully
type ScheduledTaskFinished struct {
	contract.EventMeta
	TaskName string
	Duration time.Duration
}

// Name returns the event name
func (e *ScheduledTaskFinished) Name() string {
	return "scheduler.task.completed"
}

// ScheduledTaskFailed is dispatched when a scheduled task fails
type ScheduledTaskFailed struct {
	contract.EventMeta
	TaskName string
	Err      error
	Duration time.Duration
}

// Name returns the event name
func (e *ScheduledTaskFailed) Name() string {
	return "scheduler.task.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e ScheduledTaskFailed) MarshalJSON() ([]byte, error) {
	type fields ScheduledTaskFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), errorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *ScheduledTaskFailed) UnmarshalJSON(data []byte) error {
	type fields ScheduledTaskFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = textError(v.Err)
	return nil
}

// FailureError implements contract.FailureEvent: a failed scheduled task
// has no caller observing the error, so the dispatcher bridges it to the
// error Reporter chain. It returns Err, the task's own error with its type
// (for an event decoded from its JSON form, an error with its text), or
// nil when there is none.
func (e *ScheduledTaskFailed) FailureError() error {
	return e.Err
}

// FailureSource implements contract.FailureEvent: the failure is a
// scheduled task's.
func (e *ScheduledTaskFailed) FailureSource() contract.ErrorSource {
	return contract.ErrorSourceTask
}

// dispatchScheduledTaskStarting dispatches a ScheduledTaskStarting event
func dispatchScheduledTaskStarting(events *eventemit.Emitter, ctx context.Context, name string) {
	events.EmitBuilt(ctx, func() any {
		return &ScheduledTaskStarting{
			EventMeta: runEventMeta(ctx),
			TaskName:  name,
		}
	})
}

// dispatchScheduledTaskFinished dispatches a ScheduledTaskFinished event
func dispatchScheduledTaskFinished(events *eventemit.Emitter, ctx context.Context, name string, duration time.Duration) {
	events.EmitBuilt(ctx, func() any {
		return &ScheduledTaskFinished{
			EventMeta: runEventMeta(ctx),
			TaskName:  name,
			Duration:  duration,
		}
	})
}

// dispatchScheduledTaskFailed dispatches a ScheduledTaskFailed event
func dispatchScheduledTaskFailed(events *eventemit.Emitter, ctx context.Context, name string, err error, duration time.Duration) {
	events.EmitBuilt(ctx, func() any {
		return &ScheduledTaskFailed{
			EventMeta: runEventMeta(ctx),
			TaskName:  name,
			Err:       err,
			Duration:  duration,
		}
	})
}

// Conformance: ScheduledTaskFailed participates in the failure-report bridge.
var _ contract.FailureEvent = (*ScheduledTaskFailed)(nil)

// runEventMeta returns the envelope of an event about a scheduled run under
// ctx's span, stamped now. The scheduler is in the router's dependency
// graph, so it builds the envelope from contract and trace itself.
func runEventMeta(ctx context.Context) contract.EventMeta {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	return contract.EventMeta{Context: ctx, TraceID: traceID, SpanID: spanID, ParentID: parentID, At: time.Now()}
}

// errorText returns err's text, or "" for a nil error.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return errchain.Text(err)
}

// textError returns an error with text, or nil for "".
func textError(text string) error {
	if text == "" {
		return nil
	}
	return errors.New(text)
}
