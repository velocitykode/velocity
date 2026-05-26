package queue

import "errors"

var (
	ErrNoJobAvailable    = errors.New("velocity/queue: no job available")
	ErrJobNotFound       = errors.New("velocity/queue: job not found")
	ErrBatchNotFound     = errors.New("velocity/queue: batch not found")
	ErrSigningKeyMissing = errors.New("velocity/queue: signing key not configured")
	// ErrPoisonJob is returned by a driver's Pop path when a row could not be
	// hydrated into a runnable Job (unregistered type, factory decode error)
	// AND the driver has already quarantined the offending row (moved it to
	// failed_jobs and removed it from the live queue). The worker treats this
	// as a recoverable pop error: it logs and retries, and because the poison
	// row is gone the next pop progresses to the next eligible row. Without
	// this sentinel + quarantine pair, a single poison row starved every
	// other due job because the next SELECT kept returning the same row.
	ErrPoisonJob = errors.New("velocity/queue: poison job quarantined to failed_jobs")
)
