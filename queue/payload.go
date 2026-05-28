package queue

import (
	"encoding/json"
	"fmt"
	"time"
)

// MarshalJob serializes a job into a durable [Payload]. The job's state is
// JSON-marshalled into Payload.Data so the payload bytes alone are sufficient
// to reconstruct an equivalent job on any worker in any process. There is no
// reliance on in-memory state shared with the producer.
//
// The returned Payload's Type field is normalized via [normalizeJobType], so
// pointer / package-qualified / bare Register calls all resolve to the same
// registry key on the consumer side (see queue.go).
//
// MarshalJob is the canonical entry point for persisting jobs. All durable
// drivers (Redis, database) must route through it (directly or via
// [createJobWrapper]). Tests and other intra-package callers can also use it
// directly.
//
// Returns an error when json.Marshal of the job fails (e.g. unsupported field
// types, marshaller returning an error). Callers should surface this rather
// than dropping the job: a job that cannot be marshalled cannot be retried
// across processes either.
func MarshalJob(job Job, queueName string) (*Payload, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to marshal job %T: %w", job, err)
	}

	return &Payload{
		Type:      normalizeJobType(fmt.Sprintf("%T", job)),
		Data:      data,
		Queue:     queueName,
		Attempts:  0,
		CreatedAt: time.Now(),
	}, nil
}

// HydrateJob reconstructs a Job from a persisted [Payload] using the package
// registry. It is the single source of truth for cross-process job hydration:
// any code path that needs to turn payload bytes back into a runnable Job
// (memory driver fallback, database driver pop, redis driver pop) calls this.
//
// Returns an error when the payload references an unregistered job type
// ([ErrJobNotFound]), or when the registered factory itself fails to decode
// the payload. Callers MUST surface these errors rather than substituting a
// silent stub: hydration failure is a real failure that must propagate so the
// worker can route the job to failed_jobs / event listeners.
func HydrateJob(payload *Payload) (Job, error) {
	if payload == nil {
		return nil, fmt.Errorf("velocity/queue: cannot hydrate job from nil payload")
	}
	return registry.Deserialize(payload)
}
