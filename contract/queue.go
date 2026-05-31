package contract

import (
	"context"
	"time"
)

// QueueJob represents a queue job.
type QueueJob interface {
	Handle() error
	Failed(error)
}

// QueueDriver defines the interface for queue drivers.
//
// The Ctx-suffixed methods are the primary API - they propagate the caller's
// context through to the underlying store so workers can abort in flight
// (shutdown, deadline) instead of blocking on a network round-trip. All
// driver methods are context-aware; callers thread a context through so
// cancellation flows down to the backing store.
//
// Implementations must pass queuetest.RunDriverContractTests. Drivers
// implementing optional capabilities (DedupeAwarePusher, ReservationDriver)
// must additionally pass the corresponding optional runner. See queuetest
// for the executable specification.
type QueueDriver interface {
	// PushCtx adds a job to the queue. Cancellation of ctx aborts the push
	// before it reaches the backing store (e.g. during graceful shutdown).
	PushCtx(ctx context.Context, job QueueJob, queue ...string) error

	// PushDelayedCtx adds a job to the queue with a delay.
	PushDelayedCtx(ctx context.Context, job QueueJob, delay time.Duration, queue ...string) error

	// PopCtx retrieves and removes the next job from the queue. Callers
	// typically pass a ctx tied to the worker's lifetime; when it cancels,
	// Pop returns a wrapped ctx.Err() and the worker loop exits cleanly.
	PopCtx(ctx context.Context, queue string) (QueueJob, error)

	// Size returns the number of pending jobs in the queue. Drivers MAY
	// exclude jobs currently reserved by a worker (e.g. the DB driver
	// filters reserved_at IS NULL AND failed_at IS NULL), so a freshly
	// pushed-then-reserved job may be missing from the count. Callers MUST
	// treat the result as a lower-bound monitoring signal rather than an
	// exact row count. Enforced by queuetest as Size >= pushed-and-unpopped.
	Size(queue string) (int64, error)

	// Clear removes all jobs from the queue
	Clear(queue string) error

	// Failed moves a job to the failed queue
	Failed(job QueueJob, err error, queue string) error

	// Shutdown gracefully shuts down the driver, honoring the context deadline.
	Shutdown(ctx context.Context) error
}
