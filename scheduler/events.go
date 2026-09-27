package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
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
	Context  context.Context
	TaskName string
	TraceID  string
	SpanID   string
	ParentID string
}

// Name returns the event name
func (e *ScheduledTaskStarting) Name() string {
	return "scheduler.task.started"
}

// ScheduledTaskFinished is dispatched when a scheduled task completes successfully
type ScheduledTaskFinished struct {
	Context    context.Context
	TaskName   string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *ScheduledTaskFinished) Name() string {
	return "scheduler.task.completed"
}

// ScheduledTaskFailed is dispatched when a scheduled task fails
type ScheduledTaskFailed struct {
	Context    context.Context
	TaskName   string
	Error      string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *ScheduledTaskFailed) Name() string {
	return "scheduler.task.failed"
}

// FailureError implements contract.FailureEvent: a failed scheduled task
// has no caller observing the error, so the dispatcher bridges it to the
// error Reporter chain.
func (e *ScheduledTaskFailed) FailureError() error {
	if e.Error == "" {
		return nil
	}
	return errors.New(e.Error)
}

// dispatchScheduledTaskStarting dispatches a ScheduledTaskStarting event
func dispatchScheduledTaskStarting(dispatch func(context.Context, interface{}), ctx context.Context, name string) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(ctx, &ScheduledTaskStarting{
		Context:  ctx,
		TaskName: name,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchScheduledTaskFinished dispatches a ScheduledTaskFinished event
func dispatchScheduledTaskFinished(dispatch func(context.Context, interface{}), ctx context.Context, name string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(ctx, &ScheduledTaskFinished{
		Context:    ctx,
		TaskName:   name,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// dispatchScheduledTaskFailed dispatches a ScheduledTaskFailed event
func dispatchScheduledTaskFailed(dispatch func(context.Context, interface{}), ctx context.Context, name string, err error, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(ctx, &ScheduledTaskFailed{
		Context:    ctx,
		TaskName:   name,
		Error:      errMsg,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// Conformance: ScheduledTaskFailed participates in the failure-report bridge.
var _ contract.FailureEvent = (*ScheduledTaskFailed)(nil)
