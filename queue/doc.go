// Package queue provides background job execution with pluggable storage
// drivers (memory, redis, database) configured via the QUEUE_DRIVER
// environment variable.
//
// # Optional driver capabilities
//
// Drivers MAY implement any of these to opt into framework features:
//
//	TraceAwareDriver    Persist the producer's trace ids on the wire and
//	                    return them from PopCtxWithTrace (a
//	                    ReservationDriver returns them from
//	                    PopCtxReserved), so the worker runs each job as a
//	                    new span of the producer's trace whose parent is
//	                    the producer's span. Drivers that do not implement
//	                    either fall back to PopCtx, and every job starts a
//	                    root span (a fresh trace).
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
// # Job instance
//
// Every driver states through PreservesJobInstance whether the worker runs
// the pushed Go value itself. The memory driver and queuetest.FakeQueue
// answer true: an immediate delivery, a delayed delivery and a retry all hand
// back the value that was pushed, with its pointer identity and any state
// json.Marshal does not carry (unexported fields, closures, channels, live
// clients). The redis and database drivers answer false: they store the
// marshalled payload and rebuild the job through its registered factory on
// every pop, so only marshalled state arrives. A true answer is not a promise
// that a push is accepted: the memory driver still marshals the job on push
// and refuses one json.Marshal rejects, such as a job with a closure or a
// channel in an exported field. A driver that wraps another forwards the
// answer when it delivers what the wrapped driver delivers, and answers for
// itself when it changes the delivered value.
//
// The answer is about the value handed to the driver's push. The framework's
// own wrappers push a job value of their own: the command bus pushes an
// envelope that keeps the command, and a queued event listener is pushed as
// an envelope that keeps the event and creates the listener again through its
// registered factory when the job runs. A true answer says nothing about an
// object the framework rebuilt before pushing or rebuilds when the job runs.
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
//	                    explicit name is passed to Push/PushDelayed. In a
//	                    batch a non-empty name wins over the batch's
//	                    OnQueue; an empty one takes the batch's queue.
//
//	Batchable           Sets/reads the BatchID so the worker can update
//	                    batch progress on success/failure. A batch calls
//	                    SetBatchID (and OnQueue) once per job before it is
//	                    saved; a panic in either refuses the batch whole.
//	                    The worker reads GetBatchID once per job; a panic
//	                    there fails the job for good without running it.
//
// Capability detection uses a plain type assertion at the call site
// (e.g. `if d, ok := driver.(ReservationDriver); ok { ... }`); no
// framework-level helper is provided.
//
// # Payload protection
//
// Two independent, composable layers protect persisted payloads:
//
//	Signing      HMAC-SHA256 over the marshalled payload, configured from
//	             QUEUE_SIGNING_KEY (or APP_KEY via HKDF). Integrity only:
//	             a worker refuses payloads an attacker wrote directly to
//	             the store. See signing.go.
//
//	Encryption   Opt-in via QUEUE_ENCRYPT=true. Payload.Data (the
//	             job-state JSON) is sealed with the app encryptor before
//	             persist and opened after the integrity check on pop, so
//	             jobs / failed_jobs rows and Redis lists hold ciphertext
//	             instead of plaintext job state. Envelope metadata (type,
//	             queue, attempts, trace ids) stays readable for routing.
//	             Encrypt-then-sign: the signature covers the ciphertext,
//	             so verification never touches undecrypted bytes.
//	             Requires an AEAD cipher (AES-GCM): the job type is bound
//	             into the ciphertext as AAD and non-AEAD (CBC) ciphers
//	             fail closed. See encryption.go for the deploy-transition
//	             rules; the memory driver skips encryption because its
//	             state never leaves process memory.
package queue
