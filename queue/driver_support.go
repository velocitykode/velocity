package queue

import (
	"context"
	"time"
)

// This file exposes the queue-internal helpers that out-of-tree driver
// packages (e.g. queue/redis) need to behave identically to the built-in
// drivers. They are thin, exported wrappers over the package-private
// implementations so a leaf driver living in its own package can resolve
// queue names, sign/verify payloads, deserialize via the shared default
// registry, and dispatch the same lifecycle events. The wrappers preserve
// exact behavior: they add no logic of their own.

// ResolveQueueName resolves the effective queue name for a job, applying
// the same precedence (explicit override, then job-declared queue, then
// "default") the built-in drivers use.
func ResolveQueueName(job Job, queueName ...string) string {
	return resolveQueueName(job, queueName...)
}

// SignPayload computes the HMAC-SHA256 signature for data using the
// process-wide signing key, or returns "" when signing is disabled.
func SignPayload(data []byte) string {
	return signPayload(data)
}

// VerifyPayload validates the HMAC signature of data against the
// process-wide signing key, mirroring the built-in drivers' integrity check.
func VerifyPayload(data []byte, signature string) error {
	return verifyPayload(data, signature)
}

// Deserialize converts a payload back into a Job using the shared default
// job registry (the same registry queue.Register / queue.RegisterJob
// populate) so leaf drivers resolve the identical handler set.
func Deserialize(payload *Payload) (Job, error) {
	return registry.Deserialize(payload)
}

// DispatchJobQueued dispatches a JobQueued lifecycle event through the
// supplied dispatcher (a no-op when dispatch is nil).
func DispatchJobQueued(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, delayed bool, delay time.Duration) {
	dispatchJobQueued(dispatch, ctx, jobType, queue, delayed, delay)
}

// DispatchJobFailed dispatches a JobFailed lifecycle event through the
// supplied dispatcher (a no-op when dispatch is nil).
func DispatchJobFailed(dispatch func(context.Context, interface{}), ctx context.Context, jobType, queue string, err error, duration time.Duration) {
	dispatchJobFailed(dispatch, ctx, jobType, queue, err, duration)
}
