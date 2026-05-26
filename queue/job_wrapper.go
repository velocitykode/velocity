package queue

// JobWrapper bundles a job with its persisted [Payload] for transport between
// the producer and the worker.
//
// Wire format: only Payload is serialized (the Job field is `json:"-"`). The
// Job field exists purely as an in-process fast path for the memory driver,
// which stashes the live pointer alongside the Payload so a same-process
// Pop can hand the pointer back without round-tripping through the registry.
//
// Durable drivers (database, redis) MUST NOT depend on the Job field on the
// consumer side: the deserialised wrapper they read off the wire always has
// Job == nil, and hydration goes through [HydrateJob] using Payload.Data.
// This was the C-01 bug: the database driver previously persisted only
// `{"job_id":"..."}` in Payload.Data and stashed the live Job in a
// package-global map, causing every cross-process pop to silently fall
// through to a no-op stub.
type JobWrapper struct {
	// Job is the live producer-side job pointer. Always nil after JSON
	// round-trip; only populated by [CreateJobWrapper] on the producer side
	// so the memory driver can fast-path same-process pops.
	Job     Job      `json:"-"`
	Payload *Payload `json:"payload"`
	// DedupeKey is the queue-layer deduplication identifier set by
	// PushIfNotExistsCtx. Non-empty when the wrapper was enqueued via
	// the at-most-once path; empty for ordinary Push. The memory and
	// database drivers index live entries by this key so a re-push
	// with the same key after a partial failure becomes a no-op. The
	// key is included in the JSON payload for the database / Redis
	// drivers (the Payload.DedupeKey field) so it survives a process
	// restart; this struct-level copy is the producer-side hand-off
	// to the same-process fast path.
	DedupeKey string `json:"dedupe_key,omitempty"`
}

// CreateJobWrapper builds a [JobWrapper] for a job. The job is marshalled into
// Payload.Data via [MarshalJob] so durable drivers can hydrate the job from
// payload bytes alone on any worker, in any process. The live Job pointer is
// also retained on the wrapper as an in-process fast path for the memory
// driver; it is never sent on the wire (`json:"-"`).
//
// Returns an error when the job cannot be marshalled. Callers MUST surface
// this rather than enqueueing a partially-formed wrapper: a job that cannot
// be marshalled cannot survive a cross-process pop either.
func CreateJobWrapper(job Job, queueName string) (*JobWrapper, error) {
	payload, err := MarshalJob(job, queueName)
	if err != nil {
		return nil, err
	}
	return &JobWrapper{
		Job:     job,
		Payload: payload,
	}, nil
}

// GetJobFromWrapper recovers a runnable [Job] from a wrapper.
//
// In-process fast path: when wrapper.Job is non-nil (memory driver, same
// process as the producer), it is returned directly. No registry lookup, no
// JSON round-trip, no allocation.
//
// Cross-process path: wrapper.Job is nil (the field is `json:"-"` so any
// wrapper rehydrated from a database/redis payload has lost it). The job is
// reconstructed from Payload.Data via [HydrateJob], which routes through the
// package registry. A failure here (unregistered type, factory decode error)
// is returned to the caller so the worker can route the job to failed_jobs
// instead of silently succeeding on a stub. This is the C-01 fix: the
// previous implementation looked up a package-global map and fell through
// to an empty &GenericJob{}, dropping the job in silence on cross-process
// pops.
func GetJobFromWrapper(wrapper *JobWrapper) (Job, error) {
	if wrapper == nil {
		return nil, ErrJobNotFound
	}
	if wrapper.Job != nil {
		return wrapper.Job, nil
	}
	return HydrateJob(wrapper.Payload)
}
