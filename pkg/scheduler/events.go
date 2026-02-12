package scheduler

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/pkg/trace"
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
	return "scheduled.starting"
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
	return "scheduled.finished"
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
	return "scheduled.failed"
}

// dispatchScheduledTaskStarting dispatches a ScheduledTaskStarting event
func dispatchScheduledTaskStarting(dispatch func(interface{}), ctx context.Context, name string) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&ScheduledTaskStarting{
		Context:  ctx,
		TaskName: name,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchScheduledTaskFinished dispatches a ScheduledTaskFinished event
func dispatchScheduledTaskFinished(dispatch func(interface{}), ctx context.Context, name string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&ScheduledTaskFinished{
		Context:    ctx,
		TaskName:   name,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// dispatchScheduledTaskFailed dispatches a ScheduledTaskFailed event
func dispatchScheduledTaskFailed(dispatch func(interface{}), ctx context.Context, name string, err error, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(&ScheduledTaskFailed{
		Context:    ctx,
		TaskName:   name,
		Error:      errMsg,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}
