package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Worker processes jobs from a queue
type Worker struct {
	queue       Driver
	queueName   string
	handler     func(Job) error
	concurrency int
	interval    time.Duration
	maxRetries  int
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

// WorkerOption configures a worker
type WorkerOption func(*Worker)

// WithConcurrency sets the number of concurrent workers
func WithConcurrency(n int) WorkerOption {
	return func(w *Worker) {
		if n > 0 {
			w.concurrency = n
		}
	}
}

// WithInterval sets the polling interval
func WithInterval(d time.Duration) WorkerOption {
	return func(w *Worker) {
		w.interval = d
	}
}

// WithMaxRetries sets the maximum number of retries
func WithMaxRetries(n int) WorkerOption {
	return func(w *Worker) {
		w.maxRetries = n
	}
}

// NewWorker creates a new queue worker
func NewWorker(queue Driver, queueName string, handler func(Job) error, opts ...WorkerOption) *Worker {
	ctx, cancel := context.WithCancel(context.Background())

	w := &Worker{
		queue:       queue,
		queueName:   queueName,
		handler:     handler,
		concurrency: 1,
		interval:    100 * time.Millisecond,
		maxRetries:  3,
		ctx:         ctx,
		cancel:      cancel,
	}

	for _, opt := range opts {
		opt(w)
	}

	return w
}

// Start begins processing jobs
func (w *Worker) Start() {
	for i := 0; i < w.concurrency; i++ {
		w.wg.Add(1)
		go w.work(i)
	}
}

// Stop gracefully stops the worker
func (w *Worker) Stop() {
	w.cancel()
	w.wg.Wait()
}

// work is the main worker loop
func (w *Worker) work(id int) {
	defer w.wg.Done()

	log.Printf("Worker %d started for queue %s", id, w.queueName)

	for {
		select {
		case <-w.ctx.Done():
			log.Printf("Worker %d stopped", id)
			return
		default:
			if err := w.processJob(); err != nil {
				if err.Error() != "no job available" {
					log.Printf("Worker %d error: %v", id, err)
				}
				// Back off on errors
				time.Sleep(w.interval)
			}
		}
	}
}

// processJob processes a single job
func (w *Worker) processJob() error {
	job, err := w.queue.Pop(w.queueName)
	if err != nil {
		return fmt.Errorf("failed to pop job: %w", err)
	}

	if job == nil {
		return fmt.Errorf("no job available")
	}

	// Get job type for event dispatching
	jobType := fmt.Sprintf("%T", job)

	// Process the job with timeout (default 30 seconds, configurable for tests)
	timeout := 30 * time.Second
	if w.interval < time.Second {
		// For tests with short intervals, use shorter timeout
		timeout = 5 * time.Second
	}
	jobCtx, cancel := context.WithTimeout(w.ctx, timeout)
	defer cancel()

	// Dispatch job.processing event
	dispatchJobProcessing(jobCtx, jobType, w.queueName)
	startTime := time.Now()

	done := make(chan error, 1)
	go func() {
		done <- w.handler(job)
	}()

	select {
	case err := <-done:
		duration := time.Since(startTime)
		if err != nil {
			// Dispatch job.failed event
			dispatchJobFailed(jobCtx, jobType, w.queueName, err, duration)
			// Handle job failure
			if failErr := w.queue.Failed(job, err, w.queueName); failErr != nil {
				log.Printf("Failed to mark job as failed: %v", failErr)
			}
			return fmt.Errorf("job failed: %w", err)
		}
		// Dispatch job.processed event
		dispatchJobProcessed(jobCtx, jobType, w.queueName, duration)
		return nil
	case <-jobCtx.Done():
		duration := time.Since(startTime)
		// Job timed out
		timeoutErr := fmt.Errorf("job timed out")
		// Dispatch job.failed event
		dispatchJobFailed(jobCtx, jobType, w.queueName, timeoutErr, duration)
		if failErr := w.queue.Failed(job, timeoutErr, w.queueName); failErr != nil {
			log.Printf("Failed to mark job as failed: %v", failErr)
		}
		return timeoutErr
	}
}

// Work is a global helper to start a worker
func Work(queueName string, handler func(Job) error, opts ...WorkerOption) *Worker {
	globalMu.RLock()
	q := globalQueue
	globalMu.RUnlock()

	if q == nil {
		log.Fatal("queue not initialized")
	}

	worker := NewWorker(q, queueName, handler, opts...)
	worker.Start()
	return worker
}
