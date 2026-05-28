// Package queue provides background job execution with pluggable storage
// drivers (memory, redis, database) configured via the QUEUE_DRIVER
// environment variable.
//
// # Optional driver capabilities
//
// Drivers MAY implement any of these to opt into framework features:
//
//	TraceAwareDriver    Persist APM trace ids on the wire so workers can
//	                    rebuild trace context on the consumer side via
//	                    PopCtxWithTrace. Drivers that do not implement
//	                    this fall back to PopCtx and start a fresh trace.
//
//	ReservationDriver   Lease-and-ack lifecycle: PopCtxReserved returns a
//	                    fencing token, AckCtx removes the row on success,
//	                    ReleaseCtx requeues with backoff, FailReservedCtx
//	                    moves to failed_jobs. Required for at-least-once
//	                    delivery on durable drivers; drivers that delete
//	                    on pop (memory, redis) omit this.
//
//	DedupeAwarePusher   PushIfNotExistsCtx for at-most-once enqueue keyed
//	                    by a deterministic dedupe string. Used by the
//	                    batch-callback reaper so crash-restart cycles do
//	                    not duplicate completion callbacks.
//
// # Optional job capabilities
//
// Jobs MAY implement any of these to control execution:
//
//	HandleCtxer         HandleCtx(ctx) receives the worker context for
//	                    cancellation propagation; replaces Handle() when
//	                    present.
//
//	MaxAttempter        MaxAttempts() overrides the worker's default
//	                    retry count for this job type.
//
//	Backoffer           Backoff() returns per-attempt delays; the last
//	                    value is reused beyond the slice length.
//
//	RetryDecider        ShouldRetry(err) opts out of retries for
//	                    specific error categories.
//
//	Identifiable        JobID() provides a stable key for attempt
//	                    counting across serialisation boundaries.
//
//	OnQueuer            OnQueue() selects a non-default queue when no
//	                    explicit name is passed to Push/PushDelayed.
//
//	Batchable           Sets/reads the BatchID so the worker can update
//	                    batch progress on success/failure.
//
// Capability detection uses a plain type assertion at the call site
// (e.g. `if d, ok := driver.(ReservationDriver); ok { ... }`); no
// framework-level helper is provided.
package queue
