package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
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
	backoff         BackoffStrategy
	attempts        sync.Map // keyed by jobKey(job) → *int32
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

// WithBackoff sets the backoff strategy for job retries.
// If not set, the worker uses ExponentialBackoff(1s, 5min).
func WithBackoff(strategy BackoffStrategy) WorkerOption {
	return func(w *Worker) {
		w.backoff = strategy
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

	if w.backoff == nil {
		w.backoff = ExponentialBackoff(time.Second, 5*time.Minute)
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
			w.handleJobFailure(jobCtx, job, jobType, err, duration)
			return fmt.Errorf("job failed: %w", err)
		}
		// Success — clean up attempt tracking
		w.removeAttempts(job)
		dispatchJobProcessed(w.dispatchEvent, jobCtx, jobType, w.queueName, duration)
		return nil
	case <-jobCtx.Done():
		duration := time.Since(startTime)
		timeoutErr := fmt.Errorf("job timed out")
		w.handleJobFailure(jobCtx, job, jobType, timeoutErr, duration)
		return timeoutErr
	}
}

// handleJobFailure decides whether to retry a job or permanently fail it.
func (w *Worker) handleJobFailure(ctx context.Context, job Job, jobType string, err error, duration time.Duration) {
	maxAttempts := w.maxRetries
	if ma, ok := job.(MaxAttempter); ok {
		maxAttempts = ma.MaxAttempts()
	}

	attempt := w.incrementAttempts(job)

	// Check if the job opts out of retrying this specific error
	if rd, ok := job.(RetryDecider); ok {
		if !rd.ShouldRetry(err) {
			w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts)
			return
		}
	}

	// If we have retries remaining (attempt < maxAttempts means we haven't used all attempts)
	if attempt < maxAttempts {
		backoff := w.calculateBackoff(job, attempt)
		w.logger.Info("Retrying job",
			"type", jobType,
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"backoff_ms", backoff.Milliseconds(),
			"error", err,
		)
		dispatchJobRetrying(w.dispatchEvent, ctx, jobType, w.queueName, attempt, maxAttempts, err, backoff)
		if pushErr := w.queue.PushDelayed(job, backoff, w.queueName); pushErr != nil {
			w.logger.Error("Failed to re-queue job for retry", "error", pushErr)
			w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts)
		}
		return
	}

	w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts)
}

// failJob permanently fails a job after exhausting retries.
func (w *Worker) failJob(ctx context.Context, job Job, jobType string, err error, duration time.Duration, attempt, maxAttempts int) {
	w.removeAttempts(job)
	dispatchJobFailed(w.dispatchEvent, ctx, jobType, w.queueName, err, duration)
	if failErr := w.queue.Failed(job, err, w.queueName); failErr != nil {
		w.logger.Error("Failed to mark job as failed", "error", failErr)
	}
}

// calculateBackoff determines the delay before the next retry.
func (w *Worker) calculateBackoff(job Job, attempt int) time.Duration {
	if b, ok := job.(Backoffer); ok {
		delays := b.Backoff()
		if len(delays) > 0 {
			idx := attempt - 1
			if idx >= len(delays) {
				idx = len(delays) - 1
			}
			return delays[idx]
		}
	}
	return w.backoff(attempt)
}

// jobKey returns a stable key for attempt tracking.
func (w *Worker) jobKey(job Job) interface{} {
	if id, ok := job.(Identifiable); ok {
		return id.JobID()
	}
	// Fallback to pointer identity (works for memory driver)
	return job
}

// incrementAttempts atomically increments and returns the attempt count for a job.
func (w *Worker) incrementAttempts(job Job) int {
	key := w.jobKey(job)
	val, _ := w.attempts.LoadOrStore(key, new(int32))
	counter := val.(*int32)
	return int(atomic.AddInt32(counter, 1))
}

// removeAttempts cleans up attempt tracking for a job.
func (w *Worker) removeAttempts(job Job) {
	w.attempts.Delete(w.jobKey(job))
}
