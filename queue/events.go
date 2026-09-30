package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// JobQueued is dispatched when a job is pushed to the queue
type JobQueued struct {
	contract.EventMeta
	JobType string
	Queue   string
	Delayed bool
	Delay   time.Duration
}

// Name returns the event name
func (e *JobQueued) Name() string {
	return "queue.job.queued"
}

// JobProcessing is dispatched when a worker starts processing a job
type JobProcessing struct {
	contract.EventMeta
	JobType string
	Queue   string
}

// Name returns the event name
func (e *JobProcessing) Name() string {
	return "queue.job.started"
}

// JobProcessed is dispatched when a job completes successfully
type JobProcessed struct {
	contract.EventMeta
	JobType  string
	Queue    string
	Duration time.Duration
}

// Name returns the event name
func (e *JobProcessed) Name() string {
	return "queue.job.completed"
}

// JobFailed is dispatched when a job fails
type JobFailed struct {
	contract.EventMeta
	JobType string
	Queue   string
	// JobID is the failed job's id when it has one (Identifiable), empty
	// otherwise: a job without an id, or a payload a driver could not
	// hydrate.
	JobID    string `json:",omitempty"`
	Duration time.Duration

	// Err is the failure itself: the error the job returned (or the
	// worker's timeout error), set by the worker and marked reported
	// (contract.MarkReported) when the job's own Failed hook already
	// reported it (see FailureSelfReporter). Its JSON form is its text.
	Err error
}

// Name returns the event name
func (e *JobFailed) Name() string {
	return "queue.job.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e JobFailed) MarshalJSON() ([]byte, error) {
	type fields JobFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *JobFailed) UnmarshalJSON(data []byte) error {
	type fields JobFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// FailureError implements contract.FailureEvent: a permanently failed job
// (retries exhausted) has no caller observing the error, so the dispatcher
// bridges it to the error Reporter chain. It returns Err, the job's own
// error with its type, so the rules and reporters written for jobs see it;
// the rules the error handler keys on error types for requests do not
// apply to it (see FailureSource). When Err carries the report-once marker
// (the job's Failed hook already reported the failure) the bridge's report
// gate skips it and the failure is reported once. An event decoded from
// its JSON form carries an error with Err's text; one without Err returns
// nil.
func (e *JobFailed) FailureError() error {
	return e.Err
}

// FailureSource implements contract.FailureEvent: the failure is a job's.
func (e *JobFailed) FailureSource() contract.ErrorSource {
	return contract.ErrorSourceJob
}

// JobRetrying is dispatched when a failed job is being retried
type JobRetrying struct {
	contract.EventMeta
	JobType     string
	Queue       string
	Attempt     int
	MaxAttempts int
	Err         error
	Backoff     time.Duration
}

// Name returns the event name
func (e *JobRetrying) Name() string {
	return "queue.job.retried"
}

// MarshalJSON encodes the event with Err as its text.
func (e JobRetrying) MarshalJSON() ([]byte, error) {
	type fields JobRetrying
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *JobRetrying) UnmarshalJSON(data []byte) error {
	type fields JobRetrying
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// dispatchJobQueued dispatches a JobQueued event
func dispatchJobQueued(events *eventemit.Emitter, ctx context.Context, jobType, queue string, delayed bool, delay time.Duration) bool {
	return events.EmitBuilt(ctx, func() any {
		return &JobQueued{
			EventMeta: eventmeta.Current(ctx),
			JobType:   jobType,
			Queue:     queue,
			Delayed:   delayed,
			Delay:     delay,
		}
	})
}

// dispatchJobProcessing dispatches a JobProcessing event
func dispatchJobProcessing(events *eventemit.Emitter, ctx context.Context, jobType, queue string) bool {
	return events.EmitBuilt(ctx, func() any {
		return &JobProcessing{
			EventMeta: eventmeta.Current(ctx),
			JobType:   jobType,
			Queue:     queue,
		}
	})
}

// dispatchJobProcessed dispatches a JobProcessed event
func dispatchJobProcessed(events *eventemit.Emitter, ctx context.Context, jobType, queue string, duration time.Duration) bool {
	return events.EmitBuilt(ctx, func() any {
		return &JobProcessed{
			EventMeta: eventmeta.Current(ctx),
			JobType:   jobType,
			Queue:     queue,
			Duration:  duration,
		}
	})
}

// dispatchJobFailed dispatches a JobFailed event; jobID is the job's id,
// empty when it has none.
func dispatchJobFailed(events *eventemit.Emitter, ctx context.Context, jobType, queue, jobID string, err error, duration time.Duration) bool {
	return events.EmitBuilt(ctx, func() any {
		return &JobFailed{
			EventMeta: eventmeta.Current(ctx),
			JobType:   jobType,
			Queue:     queue,
			JobID:     jobID,
			Err:       err,
			Duration:  duration,
		}
	})
}

// dispatchJobRetrying dispatches a JobRetrying event
func dispatchJobRetrying(events *eventemit.Emitter, ctx context.Context, jobType, queue string, attempt, maxAttempts int, err error, backoff time.Duration) bool {
	return events.EmitBuilt(ctx, func() any {
		return &JobRetrying{
			EventMeta:   eventmeta.Current(ctx),
			JobType:     jobType,
			Queue:       queue,
			Attempt:     attempt,
			MaxAttempts: maxAttempts,
			Err:         err,
			Backoff:     backoff,
		}
	})
}

// Conformance: JobFailed participates in the failure-report bridge.
var _ contract.FailureEvent = (*JobFailed)(nil)
