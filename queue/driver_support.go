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

// MarshalSigned marshals v, signs the marshalled bytes, and (when signing is
// enabled) calls setSig with the signature and re-marshals so the returned
// bytes carry the Signature field. The signature is computed over the UNSIGNED
// marshal; the signed bytes differ only by the added Signature field. Leaf
// drivers use this to run the identical serialize/sign/re-serialize dance the
// built-in drivers use. unsignedErrMsg and signedErrMsg are the error prefixes
// applied to the respective json.Marshal failures.
func MarshalSigned(v any, setSig func(string), unsignedErrMsg, signedErrMsg string) ([]byte, error) {
	return marshalSigned(v, setSig, unsignedErrMsg, signedErrMsg)
}

// SealPayload encrypts p.Data in place using the process-wide payload
// encryptor (no-op when encryption is disabled). Producers must call this
// BEFORE marshalling and signing the payload so the signature covers the
// ciphertext (encrypt-then-sign; see encryption.go).
func SealPayload(p *Payload) error {
	return sealPayload(p)
}

// OpenPayload decrypts p.Data in place after signature verification.
// signatureVerified must be true only when the payload carried a real
// signature that verified; it gates acceptance of legacy plaintext
// payloads while QUEUE_ENCRYPT is being rolled out (see encryption.go).
func OpenPayload(p *Payload, signatureVerified bool) error {
	return openPayload(p, signatureVerified)
}

// SealQuarantineBlob protects raw poison-payload bytes before a driver
// persists them to failed-job storage. Pass-through (sealed=false) when
// encryption is disabled; AAD-bound ciphertext, or a hash-only redaction
// stub on seal failure, when enabled. Plaintext never reaches failed
// storage while an encryptor is installed.
func SealQuarantineBlob(raw string) (string, bool) {
	return sealQuarantineBlob(raw)
}

// OpenQuarantineBlob decrypts a sealed quarantine blob for operator
// inspection. Errors when encryption is disabled or the blob is a
// redaction stub.
func OpenQuarantineBlob(blob string) ([]byte, error) {
	return openQuarantineBlob(blob)
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
