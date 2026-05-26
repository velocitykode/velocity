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

// HandleCtxer is an optional interface jobs can implement to receive
// the worker's context. When implemented, the worker calls HandleCtx(ctx)
// instead of Handle(). Cancellation of ctx (worker shutdown, per-job
// timeout) flows into the handler so long-running jobs can abort cleanly.
// If the worker's parent context carries values (deadlines, request-scoped
// values), those propagate to the handler.
//
// IMPORTANT: the handler goroutine is NOT forcibly terminated. If
// HandleCtx ignores ctx and blocks, the goroutine leaks until the process
// exits. Implementations MUST honor ctx.Done() and return promptly when
// the context is cancelled.
type HandleCtxer interface {
	HandleCtx(ctx context.Context) error
}

// Driver defines the interface for queue drivers.
//
// The Ctx-suffixed methods are the primary API — they propagate the caller's
// context through to the underlying store so workers can abort in flight
// (shutdown, deadline) instead of blocking on a network round-trip. The
// All driver methods are context-aware; callers thread a context through so
// cancellation flows down to the backing store.
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

	// Size returns the number of jobs in the queue
	Size(queue string) (int64, error)

	// Clear removes all jobs from the queue
	Clear(queue string) error

	// Failed moves a job to the failed queue
	Failed(job Job, err error, queue string) error

	// Shutdown gracefully shuts down the driver, honoring the context deadline.
	Shutdown(ctx context.Context) error
}

// Payload represents a serialized job
type Payload struct {
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
	Queue      string          `json:"queue"`
	Attempts   int             `json:"attempts"`
	CreatedAt  time.Time       `json:"created_at"`
	TraceID    string          `json:"trace_id,omitempty"`  // Producer-side APM trace ID
	SpanID     string          `json:"span_id,omitempty"`   // Producer-side APM span ID
	ParentID   string          `json:"parent_id,omitempty"` // Producer-side parent span ID
	Signature  string          `json:"signature,omitempty"` // HMAC-SHA256 integrity signature
	DatabaseID int64           `json:"-"`                   // Internal use for database driver
}

// TraceContext carries the producer-side APM trace ids associated with a
// popped job. Drivers that persist trace ids on the wire return this from
// PopCtxWithTrace so workers can rebuild the trace ctx on the consumer side.
type TraceContext struct {
	TraceID  string
	SpanID   string
	ParentID string
}

// TraceAwareDriver is an optional driver capability. Drivers that persist
// trace ids on the wire implement this so the worker can rebuild trace
// context for the per-job ctx, restoring correlation across the queue
// boundary. Workers fall back to PopCtx when a driver does not implement it.
type TraceAwareDriver interface {
	PopCtxWithTrace(ctx context.Context, queue string) (Job, TraceContext, error)
}

// ReservationToken identifies a leased row owned by the popping worker for
// the duration of handler execution. Drivers that implement [ReservationDriver]
// return a non-zero token from PopCtxReserved and accept it back on Ack /
// Release / FailReserved. A token value of zero means "no reservation"
// (e.g. drivers that delete on pop, or jobs sourced from a non-reservation
// path).
type ReservationToken int64

// ReservationDriver is an optional driver capability. Drivers that lease
// rows to workers (DB driver) implement this so the worker can defer the
// row's deletion until the handler has actually returned success. Drivers
// that already remove the entry on pop (memory, redis) do not implement
// this interface; the worker treats them as before.
//
// Lifecycle on a reservation-capable driver:
//
//  1. PopCtxReserved returns (job, token, trace, nil). The driver sets
//     reserved_at = now, reserved_by = workerID, attempts += 1 on the row.
//  2. On handler success, the worker calls AckCtx(token). Driver deletes
//     the row.
//  3. On handler failure with retries remaining, the worker calls
//     ReleaseCtx(token, backoff). Driver clears reserved_at/reserved_by and
//     pushes scheduled_at forward by backoff so the same row will be popped
//     again after the delay (in-place retry, no row churn).
//  4. On terminal failure (max attempts, opted-out retry), the worker
//     calls FailReservedCtx(token, job, err, queueName). Driver inserts the
//     failed_jobs row and deletes the original row in a single transaction.
//
// If the worker process is SIGKILLed between step 1 and any of 2/3/4, the
// row remains reserved. The next PopCtxReserved reclaims it once
// `reserved_at < now() - retryAfter`. This is the at-least-once invariant.
type ReservationDriver interface {
	// PopCtxReserved leases the next available row. Returns a non-zero
	// token on success, (nil, 0, _, nil) when no job is available.
	PopCtxReserved(ctx context.Context, queue string) (Job, ReservationToken, TraceContext, error)

	// AckCtx removes the reserved row after the handler returned success.
	AckCtx(ctx context.Context, token ReservationToken) error

	// ReleaseCtx leaves the row in place but clears its reservation and
	// pushes scheduled_at forward by delay so a future pop will reclaim
	// it as a retry. delay = 0 means "available immediately".
	ReleaseCtx(ctx context.Context, token ReservationToken, delay time.Duration) error

	// FailReservedCtx records the row in failed_jobs and deletes the
	// original row atomically, both bound to ctx.
	FailReservedCtx(ctx context.Context, token ReservationToken, job Job, jobErr error, queue string) error
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
