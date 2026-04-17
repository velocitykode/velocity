package queue

import "context"

// BatchCreated is dispatched when a new batch is created
type BatchCreated struct {
	Context   context.Context
	BatchID   string
	TotalJobs int
	Queue     string
}

// Name returns the event name
func (e *BatchCreated) Name() string { return "batch.created" }

// BatchJobCompleted is dispatched when a job in a batch completes successfully
type BatchJobCompleted struct {
	Context       context.Context
	BatchID       string
	CompletedJobs int
	TotalJobs     int
	Progress      float64
}

// Name returns the event name
func (e *BatchJobCompleted) Name() string { return "batch.job.completed" }

// BatchJobFailed is dispatched when a job in a batch fails
type BatchJobFailed struct {
	Context    context.Context
	BatchID    string
	FailedJobs int
	TotalJobs  int
	Error      string
}

// Name returns the event name
func (e *BatchJobFailed) Name() string { return "batch.job.failed" }

// BatchCompleted is dispatched when all jobs in a batch have been processed
type BatchCompleted struct {
	Context       context.Context
	BatchID       string
	TotalJobs     int
	CompletedJobs int
	FailedJobs    int
	HasFailures   bool
}

// Name returns the event name
func (e *BatchCompleted) Name() string { return "batch.completed" }

// BatchCancelled is dispatched when a batch is cancelled
type BatchCancelled struct {
	Context    context.Context
	BatchID    string
	FailedJobs int
}

// Name returns the event name
func (e *BatchCancelled) Name() string { return "batch.cancelled" }
