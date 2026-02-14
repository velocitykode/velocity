package queue

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/trace"
)

// JobQueued is dispatched when a job is pushed to the queue
type JobQueued struct {
	Context  context.Context
	JobType  string
	Queue    string
	Delayed  bool
	DelayMs  int64
	TraceID  string
	SpanID   string
	ParentID string
}

// Name returns the event name
func (e *JobQueued) Name() string {
	return "job.queued"
}

// JobProcessing is dispatched when a worker starts processing a job
type JobProcessing struct {
	Context  context.Context
	JobType  string
	Queue    string
	TraceID  string
	SpanID   string
	ParentID string
}

// Name returns the event name
func (e *JobProcessing) Name() string {
	return "job.processing"
}

// JobProcessed is dispatched when a job completes successfully
type JobProcessed struct {
	Context    context.Context
	JobType    string
	Queue      string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *JobProcessed) Name() string {
	return "job.processed"
}

// JobFailed is dispatched when a job fails
type JobFailed struct {
	Context    context.Context
	JobType    string
	Queue      string
	Error      string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *JobFailed) Name() string {
	return "job.failed"
}

// dispatchJobQueued dispatches a JobQueued event
func dispatchJobQueued(dispatch func(interface{}), ctx context.Context, jobType, queue string, delayed bool, delay time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&JobQueued{
		Context:  ctx,
		JobType:  jobType,
		Queue:    queue,
		Delayed:  delayed,
		DelayMs:  delay.Milliseconds(),
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchJobProcessing dispatches a JobProcessing event
func dispatchJobProcessing(dispatch func(interface{}), ctx context.Context, jobType, queue string) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&JobProcessing{
		Context:  ctx,
		JobType:  jobType,
		Queue:    queue,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchJobProcessed dispatches a JobProcessed event
func dispatchJobProcessed(dispatch func(interface{}), ctx context.Context, jobType, queue string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(&JobProcessed{
		Context:    ctx,
		JobType:    jobType,
		Queue:      queue,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// dispatchJobFailed dispatches a JobFailed event
func dispatchJobFailed(dispatch func(interface{}), ctx context.Context, jobType, queue string, err error, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(&JobFailed{
		Context:    ctx,
		JobType:    jobType,
		Queue:      queue,
		Error:      errMsg,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}
