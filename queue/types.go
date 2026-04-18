package queue

import (
	"context"
	"encoding/json"
	"time"
)

// Job represents a queue job
type Job interface {
	Handle() error
	Failed(error)
}

// Driver defines the interface for queue drivers.
//
// The Ctx-suffixed methods are the primary API — they propagate the caller's
// context through to the underlying store so workers can abort in flight
// (shutdown, deadline) instead of blocking on a network round-trip. The
// non-Ctx methods remain for backwards compatibility and simply forward to
// their Ctx counterparts with context.Background().
type Driver interface {
	// PushCtx adds a job to the queue. Cancellation of ctx aborts the push
	// before it reaches the backing store (e.g. during graceful shutdown).
	PushCtx(ctx context.Context, job Job, queue ...string) error

	// PushDelayedCtx adds a job to the queue with a delay.
	PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queue ...string) error

	// PopCtx retrieves and removes the next job from the queue. Callers
	// typically pass a ctx tied to the worker's lifetime; when it cancels,
	// Pop returns a wrapped ctx.Err() and the worker loop exits cleanly.
	PopCtx(ctx context.Context, queue string) (Job, error)

	// Push adds a job to the queue.
	// Deprecated: use PushCtx(ctx, job, queue...) so the caller's context
	// flows through to the store.
	Push(job Job, queue ...string) error

	// PushDelayed adds a job to the queue with a delay.
	// Deprecated: use PushDelayedCtx.
	PushDelayed(job Job, delay time.Duration, queue ...string) error

	// Pop retrieves and removes the next job from the queue.
	// Deprecated: use PopCtx so worker shutdown can abort an in-flight
	// pop instead of waiting for the blocking read to return.
	Pop(queue string) (Job, error)

	// Size returns the number of jobs in the queue
	Size(queue string) (int64, error)

	// Clear removes all jobs from the queue
	Clear(queue string) error

	// Failed moves a job to the failed queue
	Failed(job Job, err error, queue string) error

	// Shutdown gracefully shuts down the driver, honoring the context deadline.
	Shutdown(ctx context.Context) error

	// Close gracefully shuts down the driver, releasing resources.
	// Deprecated: use Shutdown(ctx) instead.
	Close() error
}

// Payload represents a serialized job
type Payload struct {
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
	Queue      string          `json:"queue"`
	Attempts   int             `json:"attempts"`
	CreatedAt  time.Time       `json:"created_at"`
	Signature  string          `json:"signature,omitempty"` // HMAC-SHA256 integrity signature
	DatabaseID int64           `json:"-"`                   // Internal use for database driver
}

// MaxAttempter is an optional interface that jobs can implement to override
// the worker's default max retry count.
type MaxAttempter interface {
	MaxAttempts() int
}

// Backoffer is an optional interface that jobs can implement to provide
// explicit per-attempt backoff delays. The last value is reused for
// subsequent attempts beyond the slice length.
type Backoffer interface {
	Backoff() []time.Duration
}

// RetryDecider is an optional interface that jobs can implement to
// opt out of retries for specific errors.
type RetryDecider interface {
	ShouldRetry(err error) bool
}

// Identifiable is an optional interface that jobs can implement to provide
// a stable key for attempt tracking across serialization boundaries
// (e.g. Redis, database drivers).
type Identifiable interface {
	JobID() string
}

// OnQueuer is an optional interface that jobs can implement to specify
// which queue they should be dispatched to. When a job implements this
// interface and no explicit queue name is passed to Push/PushDelayed,
// the value from OnQueue() is used.
type OnQueuer interface {
	OnQueue() string
}

// resolveQueueName returns the queue name for a job. Priority:
// 1. Explicit queue name passed by caller
// 2. Job's OnQueue() if it implements OnQueuer
// 3. "default"
func resolveQueueName(job Job, queueName ...string) string {
	if len(queueName) > 0 && queueName[0] != "" {
		return queueName[0]
	}
	if oq, ok := job.(OnQueuer); ok {
		if name := oq.OnQueue(); name != "" {
			return name
		}
	}
	return "default"
}
