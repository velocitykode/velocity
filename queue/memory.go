package queue

import (
	"container/heap"
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
)

// MemoryDriver implements Queue interface using in-memory storage
type MemoryDriver struct {
	mu              sync.RWMutex
	queues          map[string]*list.List
	delayed         map[string]*delayedHeap
	failed          map[string][]*failedJob
	stopChan        chan struct{}
	stopOnce        sync.Once
	wg              sync.WaitGroup
	eventDispatcher func(event interface{}) error
}

type delayedJob struct {
	wrapper *JobWrapper
	runAt   time.Time
	index   int // heap position, maintained by container/heap
}

// delayedHeap is a min-heap of *delayedJob ordered by runAt.
// Implements container/heap.Interface so ready jobs can be popped in O(log n).
type delayedHeap struct {
	items []*delayedJob
}

func (h *delayedHeap) Len() int { return len(h.items) }
func (h *delayedHeap) Less(i, j int) bool {
	return h.items[i].runAt.Before(h.items[j].runAt)
}
func (h *delayedHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index = i
	h.items[j].index = j
}
func (h *delayedHeap) Push(x any) {
	j := x.(*delayedJob)
	j.index = len(h.items)
	h.items = append(h.items, j)
}
func (h *delayedHeap) Pop() any {
	n := len(h.items)
	j := h.items[n-1]
	h.items = h.items[:n-1]
	j.index = -1
	return j
}
func (h *delayedHeap) peek() *delayedJob {
	if len(h.items) == 0 {
		return nil
	}
	return h.items[0]
}

type failedJob struct {
	wrapper  *JobWrapper
	job      Job
	error    string
	failedAt time.Time
}

// NewMemoryDriver creates a new memory queue driver.
// Call Start() to begin the background delayed-job processor.
func NewMemoryDriver() *MemoryDriver {
	return &MemoryDriver{
		queues:   make(map[string]*list.List),
		delayed:  make(map[string]*delayedHeap),
		failed:   make(map[string][]*failedJob),
		stopChan: make(chan struct{}),
	}
}

// Start begins the background goroutine that moves delayed jobs to the
// main queue when their delay has elapsed. Must be called after construction.
// The goroutine runs via async.Go so any panic is caught and logged rather
// than tearing down the process.
func (m *MemoryDriver) Start() {
	m.wg.Add(1)
	async.Go(func() {
		defer m.wg.Done()
		m.processDelayedJobs()
	})
}

// SetEventDispatcher sets the function used to dispatch events.
func (m *MemoryDriver) SetEventDispatcher(fn func(event interface{}) error) {
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *MemoryDriver) dispatchEvent(event interface{}) {
	if m.eventDispatcher != nil {
		m.eventDispatcher(event)
	}
}

// Push adds a job to the queue
func (m *MemoryDriver) Push(job Job, queueName ...string) error {
	name := resolveQueueName(job, queueName...)

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

	// Dispatch job.queued event
	dispatchJobQueued(m.dispatchEvent, context.Background(), wrapper.Payload.Type, name, false, 0)
	return nil
}

// PushDelayed adds a job to the queue with a delay.
// Delayed jobs are stored in a per-queue min-heap keyed by readyAt so the
// cleanup loop can drain ready jobs in O(log n) instead of scanning the
// full list every tick.
func (m *MemoryDriver) PushDelayed(job Job, delay time.Duration, queueName ...string) error {
	name := resolveQueueName(job, queueName...)

	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	h, exists := m.delayed[name]
	if !exists {
		h = &delayedHeap{}
		m.delayed[name] = h
	}

	heap.Push(h, &delayedJob{
		wrapper: wrapper,
		runAt:   time.Now().Add(delay),
	})

	// Dispatch job.queued event with delay info
	dispatchJobQueued(m.dispatchEvent, context.Background(), wrapper.Payload.Type, name, true, delay)
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

// Shutdown gracefully shuts down the driver, waiting for the background
// goroutine to finish. Honors the context deadline: if ctx expires before
// the goroutine exits, ctx.Err() is returned. Idempotent — safe to call
// multiple times.
func (m *MemoryDriver) Shutdown(ctx context.Context) error {
	batchStore.close() // stop package-level batch cleanup goroutine (idempotent)
	m.stopOnce.Do(func() { close(m.stopChan) })

	done := make(chan struct{})
	async.Go(func() {
		m.wg.Wait()
		close(done)
	})

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close gracefully shuts down the driver.
// Deprecated: use Shutdown(ctx) instead.
func (m *MemoryDriver) Close() error {
	return m.Shutdown(context.Background())
}

// processDelayedJobs moves delayed jobs to main queue when ready.
// Runs until stopChan is closed by Shutdown/Close. Caller decrements wg via Start().
func (m *MemoryDriver) processDelayedJobs() {
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

// moveReadyJobs pops every ready delayed job from each per-queue heap and
// appends it to the main queue. Uses heap.Pop so each promotion is O(log n);
// the previous implementation rebuilt a slice on every tick which was O(n^2)
// in the worst case.
func (m *MemoryDriver) moveReadyJobs() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	for queueName, h := range m.delayed {
		for h.Len() > 0 {
			top := h.peek()
			if top.runAt.After(now) {
				break
			}
			job := heap.Pop(h).(*delayedJob)
			q, exists := m.queues[queueName]
			if !exists {
				q = list.New()
				m.queues[queueName] = q
			}
			q.PushBack(job.wrapper)
		}

		if h.Len() == 0 {
			delete(m.delayed, queueName)
		}
	}
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
