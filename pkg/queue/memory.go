package queue

import (
	"container/list"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// MemoryDriver implements Queue interface using in-memory storage
type MemoryDriver struct {
	mu       sync.RWMutex
	queues   map[string]*list.List
	delayed  map[string][]*delayedJob
	failed   map[string][]*failedJob
	stopChan chan struct{}
	wg       sync.WaitGroup
}

type delayedJob struct {
	wrapper *JobWrapper
	runAt   time.Time
}

type failedJob struct {
	wrapper  *JobWrapper
	job      Job
	error    string
	failedAt time.Time
}

// NewMemoryDriver creates a new memory queue driver
func NewMemoryDriver() *MemoryDriver {
	m := &MemoryDriver{
		queues:   make(map[string]*list.List),
		delayed:  make(map[string][]*delayedJob),
		failed:   make(map[string][]*failedJob),
		stopChan: make(chan struct{}),
	}

	// Start background worker for delayed jobs
	m.wg.Add(1)
	go m.processDelayedJobs()

	return m
}

// Push adds a job to the queue
func (m *MemoryDriver) Push(job Job, queueName ...string) error {
	name := m.getQueueName(queueName...)

	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.queues[name]; !exists {
		m.queues[name] = list.New()
	}

	m.queues[name].PushBack(wrapper)
	return nil
}

// PushDelayed adds a job to the queue with a delay
func (m *MemoryDriver) PushDelayed(job Job, delay time.Duration, queueName ...string) error {
	name := m.getQueueName(queueName...)

	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.delayed[name]; !exists {
		m.delayed[name] = make([]*delayedJob, 0)
	}

	m.delayed[name] = append(m.delayed[name], &delayedJob{
		wrapper: wrapper,
		runAt:   time.Now().Add(delay),
	})

	return nil
}

// Pop retrieves and removes the next job from the queue
func (m *MemoryDriver) Pop(queueName string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	q, exists := m.queues[queueName]
	if !exists || q.Len() == 0 {
		return nil, nil // No jobs available
	}

	element := q.Front()
	q.Remove(element)

	wrapper, ok := element.Value.(*JobWrapper)
	if !ok {
		return nil, fmt.Errorf("invalid wrapper type")
	}

	// Return the actual job instance
	return GetJobFromWrapper(wrapper), nil
}

// Size returns the number of jobs in the queue
func (m *MemoryDriver) Size(queueName string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if q, exists := m.queues[queueName]; exists {
		return int64(q.Len()), nil
	}

	return 0, nil
}

// Clear removes all jobs from the queue
func (m *MemoryDriver) Clear(queueName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.queues, queueName)
	delete(m.delayed, queueName)

	return nil
}

// Failed moves a job to the failed queue
func (m *MemoryDriver) Failed(job Job, err error, queueName string) error {
	wrapper, serr := CreateJobWrapper(job, queueName)
	if serr != nil {
		return serr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.failed[queueName]; !exists {
		m.failed[queueName] = make([]*failedJob, 0)
	}

	m.failed[queueName] = append(m.failed[queueName], &failedJob{
		wrapper:  wrapper,
		job:      job,
		error:    err.Error(),
		failedAt: time.Now(),
	})

	// Call the job's Failed method
	job.Failed(err)

	return nil
}

// GetFailed returns all failed jobs for a queue
func (m *MemoryDriver) GetFailed(queueName string) ([]*failedJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if failed, exists := m.failed[queueName]; exists {
		return failed, nil
	}

	return []*failedJob{}, nil
}

// Close gracefully shuts down the driver
func (m *MemoryDriver) Close() error {
	close(m.stopChan)
	m.wg.Wait()
	return nil
}

// processDelayedJobs moves delayed jobs to main queue when ready
func (m *MemoryDriver) processDelayedJobs() {
	defer m.wg.Done()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.moveReadyJobs()
		case <-m.stopChan:
			return
		}
	}
}

// moveReadyJobs moves delayed jobs that are ready to the main queue
func (m *MemoryDriver) moveReadyJobs() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	for queueName, jobs := range m.delayed {
		remaining := make([]*delayedJob, 0)

		for _, job := range jobs {
			if job.runAt.Before(now) || job.runAt.Equal(now) {
				// Move to main queue
				if _, exists := m.queues[queueName]; !exists {
					m.queues[queueName] = list.New()
				}
				m.queues[queueName].PushBack(job.wrapper)
			} else {
				remaining = append(remaining, job)
			}
		}

		if len(remaining) == 0 {
			delete(m.delayed, queueName)
		} else {
			m.delayed[queueName] = remaining
		}
	}
}

// getQueueName returns the queue name or default
func (m *MemoryDriver) getQueueName(queueName ...string) string {
	if len(queueName) > 0 && queueName[0] != "" {
		return queueName[0]
	}
	return "default"
}

// GenericJob is a wrapper for jobs in memory driver
type GenericJob struct {
	Payload *Payload
}

func (g *GenericJob) Handle() error {
	// This is a placeholder - real implementation would deserialize and execute
	return nil
}

func (g *GenericJob) Failed(err error) {
	// Log the failure
}

// MarshalJSON for GenericJob
func (g *GenericJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(g.Payload)
}

// SerializeJob converts a job to a payload
func SerializeJob(job Job, queueName string) (*Payload, error) {
	// For GenericJob, we just want to store the payload
	if gj, ok := job.(*GenericJob); ok {
		return gj.Payload, nil
	}

	// Try to marshal the job
	data, err := json.Marshal(job)
	if err != nil {
		// If we can't marshal, store a simple representation
		data = []byte(fmt.Sprintf(`{"type":"%T"}`, job))
	}

	jobType := fmt.Sprintf("%T", job)

	return &Payload{
		Type:      jobType,
		Data:      data,
		Queue:     queueName,
		Attempts:  0,
		CreatedAt: time.Now(),
	}, nil
}