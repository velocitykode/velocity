package scheduler

import "errors"

var (
	ErrJobRunning = errors.New("velocity/scheduler: job already running")
)
