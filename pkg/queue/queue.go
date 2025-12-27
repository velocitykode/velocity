package queue

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/pkg/log"
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
		return nil, fmt.Errorf("no handler registered for job type: %s", payload.Type)
	}

	return handler(payload.Data)
}

// Global queue instance
var (
	globalQueue Driver
	globalMu    sync.RWMutex
	defaultName = "default"
)

// SetDefault sets the global queue instance
func SetDefault(d Driver) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalQueue = d
}

// Push adds a job to the default or specified queue
func Push(job Job, queue ...string) error {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		return fmt.Errorf("queue not initialized")
	}

	return q.Push(job, queue...)
}

// Later adds a delayed job to the queue
func Later(delay time.Duration, job Job, queue ...string) error {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		return fmt.Errorf("queue not initialized")
	}

	return q.PushDelayed(job, delay, queue...)
}

// Pop retrieves the next job from the queue
func Pop(queue string) (Job, error) {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		return nil, fmt.Errorf("queue not initialized")
	}

	return q.Pop(queue)
}

// Size returns the number of jobs in the queue
func Size(queue string) (int64, error) {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		return 0, fmt.Errorf("queue not initialized")
	}

	return q.Size(queue)
}

// Clear removes all jobs from the queue
func Clear(queue string) error {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		return fmt.Errorf("queue not initialized")
	}

	return q.Clear(queue)
}

// Reinitialize reinitializes the queue driver from environment
func Reinitialize() error {
	driver := os.Getenv("QUEUE_DRIVER")
	if driver == "" {
		driver = "memory"
	}

	var d Driver
	var err error

	switch driver {
	case "redis":
		d, err = NewRedisQueue()
		if err != nil {
			return fmt.Errorf("failed to initialize Redis queue: %w", err)
		}
	case "database":
		d, err = NewDatabaseQueue()
		if err != nil {
			return fmt.Errorf("failed to initialize database queue: %w", err)
		}
	case "memory":
		d = NewMemoryQueue()
	default:
		return fmt.Errorf("unknown queue driver: %s", driver)
	}

	SetDefault(d)
	log.Info("Queue driver reinitialized", "driver", driver)
	return nil
}