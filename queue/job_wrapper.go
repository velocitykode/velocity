package queue

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
)

// JobWrapper wraps a job with its metadata for internal storage
type JobWrapper struct {
	Job     Job             `json:"-"` // The actual job instance
	Payload *Payload        `json:"payload"`
	RawData json.RawMessage `json:"raw_data"`
}

// jobStoreEntry holds a job and its creation time for staleness tracking
type jobStoreEntry struct {
	job       Job
	createdAt time.Time
}

// jobStore is an internal store for keeping job instances in memory
type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]jobStoreEntry
	seq  uint64
}

var store = newJobStore()

func newJobStore() *jobStore {
	s := &jobStore{
		jobs: make(map[string]jobStoreEntry),
	}
	async.Go(func() { s.periodicCleanup() })
	return s
}

// periodicCleanup removes stale entries older than 1 hour
func (s *jobStore) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		cutoff := time.Now().Add(-1 * time.Hour)
		for id, entry := range s.jobs {
			if entry.createdAt.Before(cutoff) {
				delete(s.jobs, id)
			}
		}
		s.mu.Unlock()
	}
}

// StoreJob stores a job and returns its ID
func (s *jobStore) Store(job Job) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	id := fmt.Sprintf("job_%d", s.seq)
	s.jobs[id] = jobStoreEntry{job: job, createdAt: time.Now()}
	return id
}

// GetJob retrieves a job by ID
func (s *jobStore) Get(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.jobs[id]
	return entry.job, exists
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

	// Create payload with job ID. The Type field is normalized to the bare
	// type name so that pointer / package-qualified / bare Register calls all
	// resolve to the same key on the worker side.
	payload := &Payload{
		Type:      normalizeJobType(fmt.Sprintf("%T", job)),
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

// CleanupJob removes a job from the in-memory store after processing.
// Call this after a job has been processed (success or failure) to prevent memory leaks.
func CleanupJob(wrapper *JobWrapper) {
	if wrapper == nil || wrapper.Payload == nil {
		return
	}
	var data map[string]string
	if err := json.Unmarshal(wrapper.Payload.Data, &data); err == nil {
		if jobID, ok := data["job_id"]; ok {
			store.Remove(jobID)
		}
	}
}
