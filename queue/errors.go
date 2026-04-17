package queue

import "errors"

var (
	ErrNoJobAvailable    = errors.New("velocity/queue: no job available")
	ErrJobNotFound       = errors.New("velocity/queue: job not found")
	ErrBatchNotFound     = errors.New("velocity/queue: batch not found")
	ErrSigningKeyMissing = errors.New("velocity/queue: signing key not configured")
)
