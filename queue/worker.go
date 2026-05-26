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
	"github.com/velocitykode/velocity/trace"
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

// terminalCleanupTimeout bounds the DB write that records a terminal
// failure (failed_jobs INSERT + jobs DELETE) and the success ack. The
// cleanup write MUST be detached from the per-job context because the
// jobCtx-timeout branch in processJob calls into failJob with an
// already-cancelled ctx; binding the DB write to that ctx returns
// context.DeadlineExceeded immediately and the row never moves to
// failed_jobs. 5s mirrors retryPushTimeout and is generous enough for a
// healthy backend, short enough not to hang shutdown on a sick one.
const terminalCleanupTimeout = 5 * time.Second

// defaultHandlerKillCeiling bounds how long processJob will wait, after
// the per-job ctx fires, for the detached handler goroutine to return
// cooperatively. Once jobCtx.Done() fires, the goroutine is no longer
// tracked by w.wg, so without this drain Stop() returns before timed-out
// handlers complete and the goroutines accumulate unbounded.
//
// 5s mirrors retryPushTimeout: long enough for a well-behaved handler to
// observe ctx.Done() and unwind, short enough that Stop() does not hang
// on a misbehaving handler. If the ceiling is exceeded, we log a WARN
// and accept the leak; a job that ignores ctx is a bug in the handler.
//
// A var (not a const) so tests can shrink it without waiting 5s, and so a
// future worker option can override per-instance. There is no public
// setter today; consumers must rely on the default.
var defaultHandlerKillCeiling = 5 * time.Second

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
	eventDispatcher func(ctx context.Context, event interface{}) error
}

// SetEventDispatcher sets the function used to dispatch events. Safe to
// call concurrently with running pump goroutines.
func (w *Worker) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	w.mu.Lock()
	w.eventDispatcher = fn
	w.mu.Unlock()
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe per-job scoped
// values (deadline, trace ID).
func (w *Worker) dispatchEvent(ctx context.Context, event interface{}) {
	w.mu.RLock()
	fn := w.eventDispatcher
	w.mu.RUnlock()
	if fn != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		fn(ctx, event)
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
	var (
		job         Job
		producerTC  TraceContext
		reservation ReservationToken
		err         error
	)
	// Prefer the reservation-aware pop path so the row is leased (not
	// deleted) for the duration of handler execution. This is the
	// at-least-once guarantee: a SIGKILL between pop and ack leaves the
	// row reserved, and the next PopCtxReserved reclaims it after
	// retryAfter. Falls back to TraceAwareDriver and then bare PopCtx for
	// drivers that delete on pop (memory, redis).
	if rd, ok := w.queue.(ReservationDriver); ok {
		job, reservation, producerTC, err = rd.PopCtxReserved(w.ctx, w.queueName)
	} else if tad, ok := w.queue.(TraceAwareDriver); ok {
		job, producerTC, err = tad.PopCtxWithTrace(w.ctx, w.queueName)
	} else {
		job, err = w.queue.PopCtx(w.ctx, w.queueName)
	}
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

	// Restore the producer's trace ids so per-job events and HandleCtxer
	// callers see the same trace as the originating request. Empty strings
	// produce a no-op trace context, leaving legacy rows unaffected.
	if producerTC.TraceID != "" || producerTC.SpanID != "" || producerTC.ParentID != "" {
		jobCtx = trace.WithFullContext(jobCtx, producerTC.TraceID, producerTC.SpanID, producerTC.ParentID)
	}

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
			batch.checkFinished(jobCtx)
			// Ack the reservation so the row is removed; the job will
			// not be retried. Use a detached short-timeout context so a
			// slow driver does not block shutdown.
			w.ackReservation(reservation)
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
			// failed, and not routed through Failed(); the leased row stays
			// reserved and the next worker (after retryAfter) reclaims it.
			if (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) && w.ctx.Err() != nil {
				w.logger.Info("Job aborted by worker shutdown",
					"type", jobType,
					"queue", w.queueName,
					"duration_ms", duration.Milliseconds(),
				)
				return nil
			}
			// Real (non-ctx) error returned during shutdown. Routing through
			// handleJobFailure is risky: the driver is tearing down, the
			// retry push goes through a detached short-timeout context, and
			// the event dispatcher may already be closed. Log the error so
			// it is diagnosable, then abort. The driver's own shutdown path
			// (or the next worker on a durable queue) is responsible for
			// reclaiming the job. Without this log, real bugs that race
			// shutdown would vanish silently.
			if w.ctx.Err() != nil {
				w.logger.Warn("Job error swallowed during worker shutdown",
					"type", jobType,
					"queue", w.queueName,
					"job_id", jobIDOf(job),
					"error", err,
					"duration_ms", duration.Milliseconds(),
				)
				return nil
			}
			w.handleJobFailure(jobCtx, job, jobType, err, duration, reservation)
			return fmt.Errorf("velocity/queue: job failed: %w", err)
		}
		// Success: clean up attempt tracking and ack the leased row so the
		// driver removes it. For non-reservation drivers ackReservation is
		// a no-op (the row was deleted on pop).
		w.removeAttempts(job)
		// Record batch success
		if bj, ok := job.(Batchable); ok {
			if batch, found := FindBatch(bj.GetBatchID()); found {
				batch.recordSuccess(jobCtx)
			}
		}
		w.ackReservation(reservation)
		dispatchJobProcessed(w.dispatchEvent, jobCtx, jobType, w.queueName, duration)
		return nil
	case <-jobCtx.Done():
		duration := time.Since(startTime)
		// jobCtx fired: either the worker is shutting down (w.ctx cancelled,
		// which propagates to jobCtx) or the per-job timeout expired. In
		// both cases the handler goroutine is still running and is NOT
		// tracked by w.wg, so without an explicit drain it leaks past
		// Stop() and accumulates unbounded over time.
		//
		// Wait up to defaultHandlerKillCeiling for the handler to observe
		// ctx.Done() and return cooperatively. If it does not, we log a
		// WARN and accept the leak: a handler that ignores ctx is a bug,
		// and blocking Stop() forever is worse for ops than leaking one
		// goroutine.
		w.drainHandler(done, job, jobType)

		if w.ctx.Err() != nil {
			// Worker shutdown, not a real per-job timeout. Same reasoning
			// as the err-branch above: do not call handleJobFailure, do
			// not retry, do not mark Failed. Leave the row reserved; the
			// next worker reclaims it via the retryAfter predicate.
			w.logger.Info("Job aborted by worker shutdown",
				"type", jobType,
				"queue", w.queueName,
				"duration_ms", duration.Milliseconds(),
			)
			return nil
		}
		timeoutErr := fmt.Errorf("velocity/queue: job timed out")
		w.handleJobFailure(jobCtx, job, jobType, timeoutErr, duration, reservation)
		return timeoutErr
	}
}

// ackReservation deletes the leased row after handler success on
// reservation-capable drivers. It is a no-op on drivers that delete the
// row at pop time (memory, redis) and on calls with a zero token.
//
// Uses a fresh background ctx with a short timeout so the ack survives
// jobCtx cancellation (e.g. when the handler completed just as the per-
// job timeout fires) and so a slow driver cannot hold shutdown open
// past its deadline. ErrLeaseLost is downgraded to a warning: the lease
// expired and another worker reclaimed the row before we could ack, so
// the job will run a second time (the documented at-least-once cost of
// a slow handler). Other failures are logged but not propagated because
// the row will still be reclaimed by the retryAfter predicate on the
// next pop.
func (w *Worker) ackReservation(token ReservationToken) {
	if token.IsZero() {
		return
	}
	rd, ok := w.queue.(ReservationDriver)
	if !ok {
		return
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cancel()
	switch err := rd.AckCtx(ackCtx, token); {
	case err == nil:
		// happy path
	case errors.Is(err, ErrLeaseLost):
		w.logger.Warn("Lease lost before ack; another worker may re-run the job",
			"token", token.ID,
		)
	default:
		w.logger.Error("Failed to ack reserved job", "token", token.ID, "error", err)
	}
}

// drainHandler waits for the detached handler goroutine to write to done
// after jobCtx fired. Bounded by defaultHandlerKillCeiling so a misbehaving
// handler that ignores ctx cannot hang Stop() forever; in that case we log
// a warning and let the goroutine leak.
func (w *Worker) drainHandler(done <-chan error, job Job, jobType string) {
	select {
	case <-done:
		// handler returned cooperatively
	case <-time.After(defaultHandlerKillCeiling):
		w.logger.Warn("Handler goroutine did not return after ctx cancellation; leaking",
			"type", jobType,
			"queue", w.queueName,
			"job_id", jobIDOf(job),
			"kill_ceiling_ms", defaultHandlerKillCeiling.Milliseconds(),
		)
	}
}

// jobIDOf returns the job's stable ID if it implements Identifiable, or
// an empty string otherwise. Used purely for diagnostic logging; do not
// rely on this for attempt tracking (see Worker.jobKey).
func jobIDOf(job Job) string {
	if id, ok := job.(Identifiable); ok {
		return id.JobID()
	}
	return ""
}

// handleJobFailure decides whether to retry a job or permanently fail it.
//
// reservation carries the driver-side row lease for reservation-capable
// drivers (DB); on retry the row is released in place (no PushDelayedCtx
// churn), on terminal failure it is moved to failed_jobs atomically. A
// zero token falls back to the legacy PushDelayedCtx + Failed paths used
// by drivers that delete on pop.
//
// MaxAttempts source of truth: when a non-zero reservation is present,
// the persisted attempts column (carried on reservation.Attempts as the
// post-increment value observed inside the reservation transaction) is
// authoritative. The in-memory attempts cache resets on worker restart,
// so a process bounce between attempts would let an unbounded number of
// retries through; the persisted column survives the bounce. For
// drivers without reservations (memory), the worker's sync.Map cache is
// the only source available and remains in use.
func (w *Worker) handleJobFailure(ctx context.Context, job Job, jobType string, err error, duration time.Duration, reservation ReservationToken) {
	maxAttempts := w.maxRetries
	if ma, ok := job.(MaxAttempter); ok {
		maxAttempts = ma.MaxAttempts()
	}

	// Determine the attempt number for the MaxAttempts decision. Durable
	// drivers report the persisted, post-increment value on the token;
	// non-durable drivers fall through to the in-memory cache.
	attempt := w.attemptNumber(job, reservation)

	// Check if the job opts out of retrying this specific error
	if rd, ok := job.(RetryDecider); ok {
		if !rd.ShouldRetry(err) {
			w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts, reservation)
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
		// Use a detached context with a short timeout for the requeue so
		// a slow driver (Redis partition, DB lock wait) cannot hold
		// shutdown open past its deadline. If the requeue exceeds the
		// timeout the job is marked failed: losing the retry is
		// preferable to hanging the shutdown path.
		pushCtx, pushCancel := context.WithTimeout(context.Background(), retryPushTimeout)
		var requeueErr error
		if rd, ok := w.queue.(ReservationDriver); ok && !reservation.IsZero() {
			// Reservation-capable driver: release the row in place so
			// the existing row (with its attempts counter, batch ID,
			// trace ids) is reused for the retry.
			requeueErr = rd.ReleaseCtx(pushCtx, reservation, backoff)
		} else {
			// Drivers that delete on pop: enqueue a fresh delayed copy.
			requeueErr = w.queue.PushDelayedCtx(pushCtx, job, backoff, w.queueName)
		}
		pushCancel()
		if requeueErr != nil {
			if errors.Is(requeueErr, ErrLeaseLost) {
				// Lease lost between handler return and release: another
				// worker already owns the row. Do not failJob (that
				// would write a duplicate failed_jobs row for a lease
				// we no longer hold). Drop the retry; the new owner is
				// in charge.
				w.logger.Warn("Lease lost before retry release; another worker owns the row",
					"type", jobType,
					"queue", w.queueName,
					"job_id", jobIDOf(job),
				)
				return
			}
			w.logger.Error("Failed to re-queue job for retry", "error", requeueErr)
			w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts, reservation)
		}
		return
	}

	w.failJob(ctx, job, jobType, err, duration, attempt, maxAttempts, reservation)
}

// attemptNumber returns the authoritative attempt count for MaxAttempts
// decisions. For reservation-capable drivers, the persisted column value
// (carried on token.Attempts) wins because it survives worker restarts;
// the in-memory cache is bumped for parity but its return value is
// ignored. For non-reservation drivers the in-memory counter is the only
// source available.
func (w *Worker) attemptNumber(job Job, token ReservationToken) int {
	if !token.IsZero() && token.Attempts > 0 {
		// Keep the in-memory cache in sync with the persisted view so
		// it remains a usable fast-path for diagnostics and so a later
		// non-reservation call site reads a sane value. The return is
		// discarded; the persisted column wins.
		w.incrementAttempts(job)
		return token.Attempts
	}
	return w.incrementAttempts(job)
}

// failJob permanently fails a job after exhausting retries.
//
// The driver-side cleanup write MUST use a fresh context with its own
// short timeout, not the per-job ctx: when this is reached via the
// jobCtx-timeout branch in processJob, ctx is already
// context.DeadlineExceeded and any DB write bound to it returns the
// deadline error before touching the row. The row would then stay
// reserved (never moved to failed_jobs) until the lease expires,
// breaking the at-least-once-but-bounded contract.
//
// Event dispatch still uses ctx so trace ids and request-scoped values
// propagate; only the database mutation runs under the detached
// terminalCleanupTimeout budget.
func (w *Worker) failJob(ctx context.Context, job Job, jobType string, err error, duration time.Duration, attempt, maxAttempts int, reservation ReservationToken) {
	w.removeAttempts(job)
	// Record batch failure
	if bj, ok := job.(Batchable); ok {
		if batch, found := FindBatch(bj.GetBatchID()); found {
			batch.recordFailure(ctx, err)
		}
	}
	dispatchJobFailed(w.dispatchEvent, ctx, jobType, w.queueName, err, duration)

	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cleanupCancel()

	// Reservation-capable drivers record + delete the row atomically;
	// other drivers fall back to the bare Failed() path.
	if rd, ok := w.queue.(ReservationDriver); ok && !reservation.IsZero() {
		failErr := rd.FailReservedCtx(cleanupCtx, reservation, job, err, w.queueName)
		switch {
		case failErr == nil:
			// happy path
		case errors.Is(failErr, ErrLeaseLost):
			// Another worker reclaimed the row; the new owner is now
			// responsible for it. Log and move on; do not panic, do
			// not double-write failed_jobs.
			w.logger.Warn("Lease lost before terminal cleanup; another worker owns the row",
				"type", jobType,
				"queue", w.queueName,
				"job_id", jobIDOf(job),
			)
		default:
			w.logger.Error("Failed to mark reserved job as failed", "error", failErr)
		}
		return
	}
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
