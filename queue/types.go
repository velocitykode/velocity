package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Job represents a queue job
type Job = contract.QueueJob

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
type Driver = contract.QueueDriver

var (
	_ contract.QueueDriver = (*MemoryDriver)(nil)
	_ contract.QueueDriver = (*DatabaseDriver)(nil)
)

// Payload represents a serialized job
type Payload struct {
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
	Queue      string          `json:"queue"`
	Attempts   int             `json:"attempts"`
	CreatedAt  time.Time       `json:"created_at"`
	TraceID    string          `json:"trace_id,omitempty"`   // Producer-side APM trace ID
	SpanID     string          `json:"span_id,omitempty"`    // Producer-side APM span ID
	ParentID   string          `json:"parent_id,omitempty"`  // Producer-side parent span ID
	Signature  string          `json:"signature,omitempty"`  // HMAC-SHA256 integrity signature
	Encrypted  bool            `json:"encrypted,omitempty"`  // Data is sealed by the payload encryptor (see encryption.go)
	DedupeKey  string          `json:"dedupe_key,omitempty"` // Queue-layer dedupe key for at-most-once enqueue
	DatabaseID int64           `json:"-"`                    // Internal use for database driver
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
// Release / FailReserved. The zero value (ID == 0) means "no reservation"
// (e.g. drivers that delete on pop, or jobs sourced from a non-reservation
// path).
//
// Attempts is the post-increment value of the persisted attempts column
// read inside the reservation transaction. It is authoritative for
// MaxAttempts decisions on durable drivers: the in-memory sync.Map cache
// in Worker resets on process restart, so a job that crashed mid-handler
// must be capped by the persisted counter rather than the cache. Drivers
// that do not persist attempts (memory) leave this field zero; the
// worker falls back to its in-memory cache in that case.
//
// Attempts and ReservedBy together form a fencing token. The DB driver
// requires both to match the row's current state on every mutator call;
// a slow worker whose lease has expired and whose row was reclaimed by
// another worker will see attempts advanced (or reserved_by changed) on
// the row, and the mutator returns [ErrLeaseLost] instead of clobbering
// the new owner's state. attempts is strictly monotonic per row (only
// the pop path writes it, and only increases), so the tuple
// (id, attempts, reserved_by) uniquely identifies one specific lease.
type ReservationToken struct {
	// ID is the row id of the reserved record. Zero means "no reservation".
	ID int64
	// Attempts is the post-increment value of the persisted attempts
	// column as observed inside the reservation transaction. Zero means
	// "driver does not persist attempts; consult the worker cache".
	// Doubles as the monotonic component of the fencing token.
	Attempts int
	// ReservedBy is the worker identifier written to the row inside the
	// reservation transaction. Combined with Attempts it fences stale
	// tokens on AckCtx / ReleaseCtx / FailReservedCtx. Empty for
	// drivers that do not record reserver identity.
	ReservedBy string
}

// IsZero reports whether t represents the absence of a reservation
// (PopCtxReserved found no job, or the popping driver does not support
// reservations).
func (t ReservationToken) IsZero() bool { return t.ID == 0 }

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

// DedupeAwarePusher is an optional driver capability for at-most-once
// enqueue semantics keyed by a deterministic deduplication string. A
// caller (typically the batch-callback reaper) computes a stable
// dedupe key for the work it wants enqueued and asks the driver to push
// the job ONLY if no live queue row already carries that key.
//
// Contract:
//   - dedupeKey is opaque to the driver and must be < 128 bytes (DB
//     drivers map it to an indexed column).
//   - PushIfNotExistsCtx returns nil when the row is inserted AND when
//     the dedupe key matches an existing live row. Callers cannot
//     distinguish "I inserted" from "already enqueued"; this is
//     intentional because both are success outcomes for at-most-once
//     dispatch.
//   - PushIfNotExistsCtx returns a non-nil error only on transport
//     failures (DB unreachable, ctx cancelled, etc.).
//   - Dedupe state may outlive Pop/consume. Drivers retain the key
//     after the queue row is consumed (memory/database/redis keep it
//     for roughly a 7-day retention horizon) so a same-key Push during
//     that window no-ops instead of inserting a fresh row, preserving
//     at-most-once across the consume boundary. The key stops gating
//     pushes only after Clear or once the retention horizon elapses.
//
// Used by the C-03 follow-up batch callback path: deterministic
// (batchID, kind) UUIDs survive crashes between push and
// MarkCallbackDispatched. The reaper retries are at-most-once at the
// queue layer regardless of whether the bookkeeping write ran.
type DedupeAwarePusher interface {
	PushIfNotExistsCtx(ctx context.Context, job Job, dedupeKey string, queueName ...string) error
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
