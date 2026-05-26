package queue

import "errors"

var (
	ErrNoJobAvailable    = errors.New("velocity/queue: no job available")
	ErrJobNotFound       = errors.New("velocity/queue: job not found")
	ErrBatchNotFound     = errors.New("velocity/queue: batch not found")
	ErrSigningKeyMissing = errors.New("velocity/queue: signing key not configured")
	// ErrSigningKeyRequired is returned by ConfigureSigning when no signing
	// key is available in a production-like environment and the operator
	// has not explicitly opted into unsigned payloads via
	// QUEUE_ACCEPT_UNSIGNED=true. Fail-closed: an empty key combined with
	// an attacker who can write to the queue store (compromised Redis, DB
	// injection on the jobs table) lets arbitrary jobs into the worker
	// pipeline, so the boot path must refuse rather than silently warn.
	ErrSigningKeyRequired = errors.New("velocity/queue: signing key required (set QUEUE_SIGNING_KEY or APP_KEY, or set QUEUE_ACCEPT_UNSIGNED=true to acknowledge the risk)")
	// ErrPoisonJob is returned by a driver's Pop path when a row is
	// unrecoverably broken AND the driver has already quarantined it
	// (moved it to failed_jobs and removed it from the live queue). The
	// worker treats this as a recoverable pop error: it logs and retries,
	// and because the poison row is gone the next pop progresses to the
	// next eligible row. Without this sentinel + quarantine pair a single
	// poison row would head-of-line starve every other due job because
	// the next SELECT kept returning the same row.
	//
	// The sentinel deliberately covers ALL unrecoverable pop-time failure
	// modes, not just registry/hydration miss:
	//
	//   - JSON unmarshal of the on-wire wrapper failed
	//   - Marshaling the wrapper for HMAC verification failed
	//   - HMAC signature verification failed (tampered / wrong key)
	//   - Registry could not produce a Job for the payload type
	//   - The registered factory failed to decode payload.Data
	//
	// For each case the specific error is joined with ErrPoisonJob via
	// errors.Join and also persisted into failed_jobs.exception so an
	// operator can distinguish "type unregistered" from "bytes tampered"
	// without needing a second sentinel. Workers checking
	// errors.Is(err, ErrPoisonJob) only need the binary "quarantined,
	// move on" signal; the specific cause is for forensics.
	ErrPoisonJob = errors.New("velocity/queue: poison job quarantined to failed_jobs")
	// ErrLeaseLost is returned by reservation-aware mutator methods
	// (AckCtx / ReleaseCtx / FailReservedCtx) when the row's current
	// state does not match the fencing token: the lease expired and the
	// row was reclaimed by another worker. The caller must NOT retry
	// the mutation; the new owner is now responsible for the row.
	ErrLeaseLost = errors.New("velocity/queue: lease lost; row reclaimed by another worker")
)
