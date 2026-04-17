package queue

import (
	"fmt"
	"sync"
)

// Queue is an alias for Driver interface for backward compatibility
type Queue = Driver

// JobRegistry for deserializing jobs
type JobRegistry struct {
	mu       sync.RWMutex
	handlers map[string]func([]byte) (Job, error)
}

var registry = &JobRegistry{
	handlers: make(map[string]func([]byte) (Job, error)),
}

// Register registers a job type for deserialization
func Register(jobType string, handler func([]byte) (Job, error)) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.handlers[jobType] = handler
}

// Deserialize converts a payload back to a Job
func (r *JobRegistry) Deserialize(payload *Payload) (Job, error) {
	r.mu.RLock()
	handler, exists := r.handlers[payload.Type]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("velocity/queue: no handler registered for job type %s: %w", payload.Type, ErrJobNotFound)
	}

	return handler(payload.Data)
}
