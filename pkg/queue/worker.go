package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// WorkerLogger is the logging interface used by Worker.
// It is intentionally minimal to avoid coupling the queue package to a
// specific logging implementation. Any structured logger (e.g. velocity's
// log.Logger) satisfies this interface.
type WorkerLogger interface {
	Info(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// stdlibLogger adapts Go's standard log package to WorkerLogger.
type stdlibLogger struct{}

func (stdlibLogger) Info(msg string, kvs ...any)  { log.Print("[INFO] " + msg + fmtKVs(kvs)) }
func (stdlibLogger) Error(msg string, kvs ...any) { log.Print("[ERROR] " + msg + fmtKVs(kvs)) }

func fmtKVs(kvs []any) string {
	if len(kvs) == 0 {
		return ""
	}
	s := ""
	for i := 0; i+1 < len(kvs); i += 2 {
		s += fmt.Sprintf(" %v=%v", kvs[i], kvs[i+1])
	}
	return s
}

// Worker processes jobs from a queue
type Worker struct {
	queue           Driver
	queueName       string
	handler         func(Job) error
	concurrency     int
	interval        time.Duration
	timeout         time.Duration
	maxRetries      int
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	logger          WorkerLogger
	eventDispatcher func(event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events.
func (w *Worker) SetEventDispatcher(fn func(event interface{}) error) {
	w.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (w *Worker) dispatchEvent(event interface{}) {
	if w.eventDispatcher != nil {
		w.eventDispatcher(event)
	}
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

// WithTimeout sets the job processing timeout
func WithTimeout(d time.Duration) WorkerOption {
	return func(w *Worker) {
		if d > 0 {
			w.timeout = d
		}
	}
}

// WithMaxRetries sets the maximum number of retries
func WithMaxRetries(n int) WorkerOption {
	return func(w *Worker) {
		w.maxRetries = n
	}
}

// WithWorkerLogger sets the logger for the worker.
// If not set, the worker uses Go's standard log package.
func WithWorkerLogger(l WorkerLogger) WorkerOption {
	return func(w *Worker) {
		w.logger = l
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

	if w.logger == nil {
		w.logger = stdlibLogger{}
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

	w.logger.Info("Worker started", "id", id, "queue", w.queueName)

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("Worker stopped", "id", id)
			return
		default:
			if err := w.processJob(); err != nil {
				if err.Error() != "no job available" {
					w.logger.Error("Worker error", "id", id, "error", err)
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

	// Process the job with timeout
	timeout := w.timeout
	if timeout == 0 {
		timeout = 30 * time.Second
		if w.interval < time.Second {
			// For tests with short intervals, use shorter timeout
			timeout = 5 * time.Second
		}
	}
	jobCtx, cancel := context.WithTimeout(w.ctx, timeout)
	defer cancel()

	// Dispatch job.processing event
	dispatchJobProcessing(w.dispatchEvent, jobCtx, jobType, w.queueName)
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
			dispatchJobFailed(w.dispatchEvent, jobCtx, jobType, w.queueName, err, duration)
			// Handle job failure
			if failErr := w.queue.Failed(job, err, w.queueName); failErr != nil {
				w.logger.Error("Failed to mark job as failed", "error", failErr)
			}
			return fmt.Errorf("job failed: %w", err)
		}
		// Dispatch job.processed event
		dispatchJobProcessed(w.dispatchEvent, jobCtx, jobType, w.queueName, duration)
		return nil
	case <-jobCtx.Done():
		duration := time.Since(startTime)
		// Job timed out
		timeoutErr := fmt.Errorf("job timed out")
		// Dispatch job.failed event
		dispatchJobFailed(w.dispatchEvent, jobCtx, jobType, w.queueName, timeoutErr, duration)
		if failErr := w.queue.Failed(job, timeoutErr, w.queueName); failErr != nil {
			w.logger.Error("Failed to mark job as failed", "error", failErr)
		}
		return timeoutErr
	}
}
