package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// MaxWorkerConcurrency is the upper bound for WithConcurrency.
// Values above this are clamped to prevent accidentally spawning an
// unreasonable number of goroutines on mis-typed configuration.
const MaxWorkerConcurrency = 10_000

// Logger is the minimal logging interface used by queue internals
// (workers, memory driver, signing). The framework's log.Logger satisfies
// this shape; keeping the contract local lets queue/ remain a log-free leaf.
type Logger interface {
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// WorkerLogger is the consumer-facing alias used by WithWorkerLogger.
// Identical to Logger so that worker code, fallback impls, and external
// adapters share one method set (Info/Warn/Error). The framework's
// *log.Logger satisfies it directly.
type WorkerLogger = Logger

// nullLogger is an explicit silent sink. It is only installed when a caller
// explicitly opts in via WithWorkerLogger(nullLogger{}); the implicit fallback
// in NewWorker uses stderrLogger so that worker errors are never invisible.
type nullLogger struct{}

func (nullLogger) Info(string, ...any)  {}
func (nullLogger) Warn(string, ...any)  {}
func (nullLogger) Error(string, ...any) {}

// stderrFallback holds the io.Writer used by stderrLogger and the
// construction warning. It is wrapped in an atomic.Value so test code that
// redirects output cannot race with concurrent writeStderr readers.
var stderrFallback atomic.Value // holds stderrWriter

type stderrWriter struct{ io.Writer }

func init() {
	stderrFallback.Store(stderrWriter{Writer: os.Stderr})
}

func stderrFallbackWriter() io.Writer {
	return stderrFallback.Load().(stderrWriter).Writer
}

// stderrLogger is the implicit fallback installed by NewWorker when no
// WorkerLogger option is supplied. It writes structured key/value lines to
// stderrFallback so that operators always see worker errors even when no
// framework logger has been wired. Library code printing to stderr on
// catastrophic-silent-loss paths is preferable to dropping jobs in silence.
type stderrLogger struct{}

func (stderrLogger) Info(msg string, kvs ...any)  { writeStderr("INFO", msg, kvs) }
func (stderrLogger) Warn(msg string, kvs ...any)  { writeStderr("WARN", msg, kvs) }
func (stderrLogger) Error(msg string, kvs ...any) { writeStderr("ERROR", msg, kvs) }

// stderrWriteMu serializes writeStderr emission so concurrent pump goroutines
// cannot interleave bytes within a single line. os.Stderr offers per-syscall
// atomicity on POSIX but not line-atomicity, and a redirected *bytes.Buffer
// offers neither. Errors are rare; the lock cost is negligible.
var stderrWriteMu sync.Mutex

func writeStderr(level, msg string, kvs []any) {
	var b []byte
	b = append(b, "velocity/queue ["...)
	b = append(b, level...)
	b = append(b, "] "...)
	b = append(b, msg...)
	for i := 0; i+1 < len(kvs); i += 2 {
		b = append(b, ' ')
		b = fmt.Appendf(b, "%v=%v", kvs[i], kvs[i+1])
	}
	if len(kvs)%2 == 1 {
		b = fmt.Appendf(b, " %v=MISSING", kvs[len(kvs)-1])
	}
	b = append(b, '\n')
	stderrWriteMu.Lock()
	_, _ = stderrFallbackWriter().Write(b)
	stderrWriteMu.Unlock()
}

// retryPushTimeout bounds how long the worker will wait when re-queueing
// a failed job for retry. It is intentionally short so that a slow driver
// (e.g. Redis partition, DB lock) cannot hold shutdown open. If the retry
// push exceeds this budget the job is marked failed instead of requeued.
const retryPushTimeout = 5 * time.Second

// Worker processes jobs from a queue
type Worker struct {
	queue       Driver
	queueName   string
	handler     func(Job) error
	concurrency int
	interval    time.Duration
	timeout     time.Duration
	maxRetries  int
	backoff     BackoffStrategy
	attempts    sync.Map // keyed by jobKey(job) → *int32
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	logger      WorkerLogger

	// mu guards eventDispatcher. The setter is exposed publicly via
	// SetEventDispatcher and may be called concurrently with pump goroutines
	// that read the dispatcher to fire job lifecycle events. Without this
	// guard the read/write race is reachable any time wireInstanceEvents
	// runs after Start, and in tests that reassign the dispatcher between
	// fixtures.
	mu              sync.RWMutex
	eventDispatcher func(event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events. Safe to
// call concurrently with running pump goroutines.
func (w *Worker) SetEventDispatcher(fn func(event interface{}) error) {
	w.mu.Lock()
	w.eventDispatcher = fn
	w.mu.Unlock()
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (w *Worker) dispatchEvent(event interface{}) {
	w.mu.RLock()
	fn := w.eventDispatcher
	w.mu.RUnlock()
	if fn != nil {
		fn(event)
	}
}

// Option configures a worker
type Option func(*Worker)

// WithConcurrency sets the number of concurrent workers.
// Values <= 0 are ignored; values above MaxWorkerConcurrency are clamped
// to MaxWorkerConcurrency to protect against misconfiguration.
func WithConcurrency(n int) Option {
	return func(w *Worker) {
		if n <= 0 {
			return
		}
		if n > MaxWorkerConcurrency {
			n = MaxWorkerConcurrency
		}
		w.concurrency = n
	}
}

// WithInterval sets the polling interval
func WithInterval(d time.Duration) Option {
	return func(w *Worker) {
		w.interval = d
	}
}

// WithTimeout sets the job processing timeout
func WithTimeout(d time.Duration) Option {
	return func(w *Worker) {
		if d > 0 {
			w.timeout = d
		}
	}
}

// WithMaxRetries sets the maximum number of retries
func WithMaxRetries(n int) Option {
	return func(w *Worker) {
		w.maxRetries = n
	}
}

// WithBackoff sets the backoff strategy for job retries.
// If not set, the worker uses ExponentialBackoff(1s, 5min).
func WithBackoff(strategy BackoffStrategy) Option {
	return func(w *Worker) {
		w.backoff = strategy
	}
}

// WithWorkerLogger sets the logger for the worker. When not set, NewWorker
// installs stderrLogger as the implicit fallback and emits a per-construction
// warning to stderr, so internal worker errors are never invisible. Pass
// WithWorkerLogger(nullLogger{}) to opt into silence explicitly.
func WithWorkerLogger(l WorkerLogger) Option {
	return func(w *Worker) {
		w.logger = l
	}
}

// NewWorker creates a new queue worker. The worker is inert until Start is
// called: no background goroutines are spawned and no context is bound
// until then, so callers are free to construct a Worker and wire it into a
// bootstrap sequence without creating an orphaned context.
func NewWorker(queue Driver, queueName string, handler func(Job) error, opts ...Option) *Worker {
	w := &Worker{
		queue:       queue,
		queueName:   queueName,
		handler:     handler,
		concurrency: 1,
		interval:    100 * time.Millisecond,
		maxRetries:  3,
	}

	for _, opt := range opts {
		opt(w)
	}

	if w.backoff == nil {
		w.backoff = ExponentialBackoff(time.Second, 5*time.Minute)
	}

	if w.logger == nil {
		fmt.Fprintf(stderrFallbackWriter(),
			"velocity/queue: NewWorker(queue=%s) constructed without WithWorkerLogger; "+
				"falling back to stderr. Pass queue.WithWorkerLogger(s.Log) to route "+
				"worker errors through the framework logger.\n",
			queueName)
		w.logger = stderrLogger{}
	}

	return w
}

// Start begins processing jobs. The parent context controls the worker's
// lifecycle: when it cancels, all pump goroutines observe cancellation
// through the internal worker context and drain via Stop-style semantics.
// This lets application-level shutdown contexts (e.g. App.Shutdown) flow
// through to job-execution contexts without requiring a separate Stop call.
//
// Passing a nil context is equivalent to context.Background(); the worker
// then only exits when Stop is invoked.
//
// Each pump goroutine is wrapped via async.Go so any unrecovered panic in
// processJob or the handler is reported via the framework panic logger
// instead of tearing down the process.
//
// Start is idempotent: a second call while the worker is already running
// is a no-op.
func (w *Worker) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if w.ctx != nil {
		// Already started: do not spawn additional pumps.
		return
	}
	w.ctx, w.cancel = context.WithCancel(ctx)

	for i := 0; i < w.concurrency; i++ {
		w.wg.Add(1)
		id := i
		async.Go(func() {
			defer w.wg.Done()
			w.work(id)
		})
	}
}

// Stop gracefully stops the worker. Safe to call before Start (no-op) or
// multiple times.
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

// work is the main worker loop. Caller is responsible for wg bookkeeping
// via async.Go in Start().
func (w *Worker) work(id int) {
	w.logger.Info("Worker started", "id", id, "queue", w.queueName)

	for {
		select {
		case <-w.ctx.Done():
			w.logger.Info("Worker stopped", "id", id)
			return
		default:
			if err := w.processJob(); err != nil {
				if !errors.Is(err, ErrNoJobAvailable) {
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
	job, err := w.queue.PopCtx(w.ctx, w.queueName)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to pop job: %w", err)
	}

	if job == nil {
		return ErrNoJobAvailable
	}

	// Get job type for event dispatching. Normalized to match the registry
	// key and persisted Payload.Type so observability across drivers, events,
	// and registry lookups all reference the same identifier.
	jobType := normalizeJobType(fmt.Sprintf("%T", job))

	// Process the job with timeout. Callers that need a different default
	// for tests should inject their own timeout with WithTimeout, their own
	// clock, or cancel the worker context directly; the driver no longer
	// second-guesses the value based on polling interval.
	timeout := w.timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	jobCtx, cancel := context.WithTimeout(w.ctx, timeout)
	defer cancel()

	// Dispatch job.processing event
	dispatchJobProcessing(w.dispatchEvent, jobCtx, jobType, w.queueName)
	startTime := time.Now()

	// Check if this is a cancelled batch job, skip processing.
	// Note: This is a best-effort check. A batch could be cancelled between this
	// check and job execution (TOCTOU), so cancellation is not guaranteed to prevent
	// a job from running. This is an acceptable trade-off for simplicity.
	if bj, ok := job.(Batchable); ok {
		if batch, found := FindBatch(bj.GetBatchID()); found && batch.Cancelled() {
			// Decrement pending so the batch can still reach Finished state
			batch.pendingJobs.Add(-1)
			batch.checkFinished()
			return nil
		}
	}

	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- panicerr.FromRecovered(r)
			}
		}()
		// If the job implements HandleCtxer, invoke it directly with the
		// worker's per-job context so cancellation (worker shutdown, per-job
		// timeout) flows into the handler. Otherwise fall back to the
		// user-supplied handler, which typically calls job.Handle().
		if hc, ok := job.(HandleCtxer); ok {
			done <- hc.HandleCtx(jobCtx)
			return
		}
		done <- w.handler(job)
	}()

	select {
	case err := <-done:
		duration := time.Since(startTime)
		if err != nil {
			// If the worker itself is shutting down and the handler returned
			// a context error, treat this as a clean abort rather than a job
			// failure: the worker asked the job to stop, the job didn't fail.
			// We discriminate against jobCtx.Done() (per-job timeout) by
			// checking w.ctx.Err(): only the parent worker context being
			// done counts as shutdown. The job is not retried, not marked
			// failed, and not routed through Failed(); it stays "in flight"
			// from the driver's perspective and a future worker will pick it
			// up (or the driver's own shutdown path handles it).
			if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && w.ctx.Err() != nil {
				w.logger.Info("Job aborted by worker shutdown",
					"type", jobType,
					"queue", w.queueName,
					"duration_ms", duration.Milliseconds(),
				)
				return nil
			}
			w.handleJobFailure(jobCtx, job, jobType, err, duration)
			return fmt.Errorf("velocity/queue: job failed: %w", err)
		}
		// Success: clean up attempt tracking
		w.removeAttempts(job)
		// Record batch success
		if bj, ok := job.(Batchable); ok {
			if batch, found := FindBatch(bj.GetBatchID()); found {
				batch.recordSuccess()
			}
		}
		dispatchJobProcessed(w.dispatchEvent, jobCtx, jobType, w.queueName, duration)
		return nil
	case <-jobCtx.Done():
		duration := time.Since(startTime)
		// Distinguish worker shutdown from a real per-job timeout. jobCtx is
		// derived from w.ctx, so a worker Stop() cancels jobCtx as well; in
		// that case the job is not failing, the worker is leaving. Same
		// reasoning as the err-branch above: do not call handleJobFailure,
		// do not retry, do not mark Failed. The handler goroutine will be
		// allowed to finish on its own (it MUST honor ctx.Done(); see
		// HandleCtxer godoc).
		if w.ctx.Err() != nil {
			w.logger.Info("Job aborted by worker shutdown",
				"type", jobType,
				"queue", w.queueName,
				"duration_ms", duration.Milliseconds(),
			)
			return nil
		}
		timeoutErr := fmt.Errorf("velocity/queue: job timed out")
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
		// Use a detached context with a short timeout for the retry push so a
		// slow driver (Redis partition, DB lock wait) cannot hold shutdown open
		// past its deadline. If the push exceeds the timeout the job is marked
		// failed: losing the retry is preferable to hanging the shutdown path.
		pushCtx, pushCancel := context.WithTimeout(context.Background(), retryPushTimeout)
		pushErr := w.queue.PushDelayedCtx(pushCtx, job, backoff, w.queueName)
		pushCancel()
		if pushErr != nil {
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
	// Record batch failure
	if bj, ok := job.(Batchable); ok {
		if batch, found := FindBatch(bj.GetBatchID()); found {
			batch.recordFailure(err)
		}
	}
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
