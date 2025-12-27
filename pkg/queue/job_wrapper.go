package queue

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// JobWrapper wraps a job with its metadata for internal storage
type JobWrapper struct {
	Job      Job             `json:"-"` // The actual job instance
	Payload  *Payload        `json:"payload"`
	RawData  json.RawMessage `json:"raw_data"`
}

// jobStore is an internal store for keeping job instances in memory
type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]Job
	seq  uint64
}

var store = &jobStore{
	jobs: make(map[string]Job),
}

// StoreJob stores a job and returns its ID
func (s *jobStore) Store(job Job) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := fmt.Sprintf("job_%d", s.seq)
	s.jobs[id] = job
	return id
}

// GetJob retrieves a job by ID
func (s *jobStore) Get(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, exists := s.jobs[id]
	return job, exists
}

// RemoveJob removes a job from the store
func (s *jobStore) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.jobs, id)
}

// CreateJobWrapper creates a wrapper for a job
func CreateJobWrapper(job Job, queueName string) (*JobWrapper, error) {
	// Store the job instance
	jobID := store.Store(job)

	// Create payload with job ID
	payload := &Payload{
		Type:      fmt.Sprintf("%T", job),
		Data:      []byte(fmt.Sprintf(`{"job_id":"%s"}`, jobID)),
		Queue:     queueName,
		Attempts:  0,
		CreatedAt: time.Now(),
	}

	// Try to marshal the job data for reconstruction
	rawData, _ := json.Marshal(job)

	return &JobWrapper{
		Job:     job,
		Payload: payload,
		RawData: rawData,
	}, nil
}

// GetJobFromWrapper retrieves the job from a wrapper
func GetJobFromWrapper(wrapper *JobWrapper) Job {
	if wrapper.Job != nil {
		return wrapper.Job
	}

	// Try to extract job ID from payload
	var data map[string]string
	if err := json.Unmarshal(wrapper.Payload.Data, &data); err == nil {
		if jobID, ok := data["job_id"]; ok {
			if job, exists := store.Get(jobID); exists {
				return job
			}
		}
	}

	// Fallback to GenericJob
	return &GenericJob{Payload: wrapper.Payload}
}