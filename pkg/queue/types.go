package queue

import (
	"encoding/json"
	"time"
)

// Job represents a queue job
type Job interface {
	Handle() error
	Failed(error)
}

// Driver defines the interface for queue drivers
type Driver interface {
	// Push adds a job to the queue
	Push(job Job, queue ...string) error

	// PushDelayed adds a job to the queue with a delay
	PushDelayed(job Job, delay time.Duration, queue ...string) error

	// Pop retrieves and removes the next job from the queue
	Pop(queue string) (Job, error)

	// Size returns the number of jobs in the queue
	Size(queue string) (int64, error)

	// Clear removes all jobs from the queue
	Clear(queue string) error

	// Failed moves a job to the failed queue
	Failed(job Job, err error, queue string) error
}

// Payload represents a serialized job
type Payload struct {
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
	Queue      string          `json:"queue"`
	Attempts   int             `json:"attempts"`
	CreatedAt  time.Time       `json:"created_at"`
	DatabaseID int64           `json:"-"` // Internal use for database driver
}