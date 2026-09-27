package queue

import (
	"context"
	"errors"
	"time"

	"github.com/velocitykode/velocity/contract"
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
	return "queue.job.queued"
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
	return "queue.job.started"
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
	return "queue.job.completed"
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

	// Err is the failure itself, where Error is its text: the error the job
	// returned (or the worker's timeout error), set by the worker and
	// marked reported (contract.MarkReported) when the job's own Failed hook
	// already reported it (see FailureSelfReporter).
	// It is not serialized: the JSON form keeps Error alone.
	Err error `json:"-"`
}

// Name returns the event name
func (e *JobFailed) Name() string {
	return "queue.job.failed"
}

// FailureError implements contract.FailureEvent: a permanently failed job
// (retries exhausted) has no caller observing the error, so the dispatcher
// bridges it to the error Reporter chain. It returns Err, the job's own
// error with its type, so the rules and reporters written for jobs see it;
// the rules the error handler keys on error types for requests do not
// apply to it (see FailureSource). When Err carries the report-once marker
// (the job's Failed hook already reported the failure) the bridge's report
// gate skips it and the failure is reported once. An event without Err
// (one decoded from its JSON form) returns a new error with the Error
// text, or nil when there is none.
func (e *JobFailed) FailureError() error {
	if e.Err != nil {
		return e.Err
	}
	if e.Error == "" {
		return nil
	}
	return errors.New(e.Error)
}

// FailureSource implements contract.FailureEvent: the failure is a job's.
func (e *JobFailed) FailureSource() contract.ErrorSource {
	return contract.ErrorSourceJob
}

// JobRetrying is dispatched when a failed job is being retried
type JobRetrying struct {
	Context     context.Context
	JobType     string
	Queue       string
	Attempt     int
	MaxAttempts int
	Error       string
	BackoffMs   int64
	TraceID     string
	SpanID      string
	ParentID    string
}

// Name returns the event name
func (e *JobRetrying) Name() string {
	return "queue.job.retried"
}

// dispatchJobQueued dispatches a JobQueued event
func dispatchJobQueued(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, delayed bool, delay time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(ctx, &JobQueued{
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
func dispatchJobProcessing(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(ctx, &JobProcessing{
		Context:  ctx,
		JobType:  jobType,
		Queue:    queue,
		TraceID:  traceID,
		SpanID:   spanID,
		ParentID: parentID,
	})
}

// dispatchJobProcessed dispatches a JobProcessed event
func dispatchJobProcessed(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatch(ctx, &JobProcessed{
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
func dispatchJobFailed(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, err error, duration time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(ctx, &JobFailed{
		Context:    ctx,
		JobType:    jobType,
		Queue:      queue,
		Error:      errMsg,
		Err:        err,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}

// dispatchJobRetrying dispatches a JobRetrying event
func dispatchJobRetrying(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, attempt, maxAttempts int, err error, backoff time.Duration) {
	if dispatch == nil {
		return
	}
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	dispatch(ctx, &JobRetrying{
		Context:     ctx,
		JobType:     jobType,
		Queue:       queue,
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		Error:       errMsg,
		BackoffMs:   backoff.Milliseconds(),
		TraceID:     traceID,
		SpanID:      spanID,
		ParentID:    parentID,
	})
}

// Conformance: JobFailed participates in the failure-report bridge.
var _ contract.FailureEvent = (*JobFailed)(nil)
