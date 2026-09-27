package queue

import (
	"encoding/json"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// BatchCreated is dispatched when a new batch is created
type BatchCreated struct {
	contract.EventMeta
	BatchID   string
	TotalJobs int
	Queue     string
}

// Name returns the event name
func (e *BatchCreated) Name() string { return "queue.batch.created" }

// BatchJobCompleted is dispatched when a job in a batch completes successfully
type BatchJobCompleted struct {
	contract.EventMeta
	BatchID       string
	CompletedJobs int
	TotalJobs     int
	Progress      float64
}

// Name returns the event name
func (e *BatchJobCompleted) Name() string { return "queue.batch.job.completed" }

// BatchJobFailed is dispatched when a job in a batch fails
type BatchJobFailed struct {
	contract.EventMeta
	BatchID    string
	FailedJobs int
	TotalJobs  int
	Err        error
}

// Name returns the event name
func (e *BatchJobFailed) Name() string { return "queue.batch.job.failed" }

// MarshalJSON encodes the event with Err as its text.
func (e BatchJobFailed) MarshalJSON() ([]byte, error) {
	type fields BatchJobFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *BatchJobFailed) UnmarshalJSON(data []byte) error {
	type fields BatchJobFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// BatchCompleted is dispatched when all jobs in a batch have been processed
type BatchCompleted struct {
	contract.EventMeta
	BatchID       string
	TotalJobs     int
	CompletedJobs int
	FailedJobs    int
	HasFailures   bool
}

// Name returns the event name
func (e *BatchCompleted) Name() string { return "queue.batch.completed" }

// BatchCancelled is dispatched when a batch is cancelled
type BatchCancelled struct {
	contract.EventMeta
	BatchID    string
	FailedJobs int
}

// Name returns the event name
func (e *BatchCancelled) Name() string { return "queue.batch.cancelled" }
