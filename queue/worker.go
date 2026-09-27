package queue

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// MaxWorkerConcurrency is the upper bound for WithConcurrency.
// Values above this are clamped to prevent accidentally spawning an
// unreasonable number of goroutines on mis-typed configuration.
const MaxWorkerConcurrency = 10_000

// nullLogger is an explicit silent sink, for tests that opt into silence
// with WithWorkerLogger(nullLogger{}).
type nullLogger struct{}

func (nullLogger) Debug(string, ...any) {}
func (nullLogger) Info(string, ...any)  {}
func (nullLogger) Warn(string, ...any)  {}
func (nullLogger) Error(string, ...any) {}
func (nullLogger) Fatal(string, ...any) {}

// With returns the silent sink itself.
func (n nullLogger) With(...any) contract.Logger { return n }

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
	logger      contract.Logger

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
	if fn := w.currentEventDispatcher(); fn != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		fn(ctx, event)
	}
}

// currentEventDispatcher returns the dispatcher SetEventDispatcher set, or
// nil.
func (w *Worker) currentEventDispatcher() func(ctx context.Context, event interface{}) error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.eventDispatcher
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

// WithWorkerLogger sets the logger for the worker. When it is not set, or
// set to nil, the worker writes through the framework's standalone fallback
// logger, which writes warnings and errors to standard error, so internal
// worker errors are never invisible.
func WithWorkerLogger(l contract.Logger) Option {
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

	w.logger = fallbacklog.Resolve(w.logger)

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
				// A failed job was already retried, or failed for good and
				// reported or logged once (see failJob); only a worker
				// error of its own is logged here.
				var failed *jobFailedError
				if !errors.Is(err, ErrNoJobAvailable) && !errors.As(err, &failed) {
					w.logger.Error("Worker error", "id", id, "error", err)
				}
				// Back off on errors
				time.Sleep(w.interval)
			}
		}
	}
}

// jobFailedError is what processJob returns for a job that failed or timed
// out: handleJobFailure has already retried it, or failed it for good and
// reported or logged the failure, so the work loop backs off without
// logging it again. It is transparent: same text, and Unwrap exposes the
// failure.
type jobFailedError struct {
	err error
}

func (e *jobFailedError) Error() string { return e.err.Error() }
func (e *jobFailedError) Unwrap() error { return e.err }

// jobLogger returns the worker's logger bound to one job: its type under
// job_type, the queue, its id under job_id when it has one, and the
// request, trace and span ids of ctx, the context the job runs under
// (trace.LogFields).
func (w *Worker) jobLogger(ctx context.Context, job Job, jobType string) contract.Logger {
	fields := []any{"job_type", jobType, "queue", w.queueName}
	if id := jobIDOf(job); id != "" {
		fields = append(fields, "job_id", id)
	}
	return w.logger.With(append(fields, trace.LogFields(ctx)...)...)
}

// processJob processes a single job. Every line it and the functions it
// calls write for the job goes through the job's logger (jobLogger).
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

	// Run the job as a new span of the producer's trace whose parent is the
	// producer's span, so per-job events and HandleCtxer callers join the
	// originating request's trace. A payload with no trace ids (a producer
	// without a trace, a row written before trace ids were persisted) and a
	// driver without trace support leave producerTC empty: the job starts a
	// root span, never inheriting whatever trace the worker's own context
	// carries.
	jobCtx = trace.StartSpan(jobCtx, trace.Parent{TraceID: producerTC.TraceID, SpanID: producerTC.SpanID})
	log := w.jobLogger(jobCtx, job, jobType)

	// Dispatch queue.job.started event
	dispatchJobProcessing(w.dispatchEvent, jobCtx, jobType, w.queueName)
	startTime := time.Now()

	// Check if this is a cancelled batch job, skip processing.
	// Note: This is a best-effort check. A batch could be cancelled between this
	// check and job execution (TOCTOU), so cancellation is not guaranteed to prevent
	// a job from running. This is an acceptable trade-off for simplicity.
	//
	// Counter mutation goes through the repository so a worker on a
	// different process than the dispatcher still updates the shared
	// pending_jobs counter; see queue/batch_repository.go for the
	// in-memory and database implementations.
	if bj, ok := job.(Batchable); ok {
		if batch, found := FindBatch(bj.GetBatchID()); found && batch.Cancelled() {
			// Ack first; the batch counter decrement is a side effect
			// and must only fire on the worker that actually owned the
			// lease. If the lease was lost, the new owner will run the
			// same skip-and-ack path and decrement the counter once.
			// (C-02: side effects only after a confirmed fenced ack.)
			if owned := w.ackReservation(log, reservation); !owned {
				return nil
			}
			// (C-03: batch.recordSkip routes the pendingJobs decrement
			// through the BatchRepository so a worker in a different
			// process than the dispatcher mutates the shared SQL row,
			// not a process-local map. The repository's CAS gates the
			// terminal-completion side effects.)
			batch.recordSkip(jobCtx)
			return nil
		}
	}

	done := make(chan error, 1)
	// Not async.Go: must forward a recovered panic value through `done`
	// so the outer select reports it as the job error (and counts toward
	// retries) instead of swallowing it into the package logger only.
	go func() { //safe-goroutine: forwards panic via done for retry accounting, see comment above
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
				log.Info("Job aborted by worker shutdown",
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
				log.Warn("Job error swallowed during worker shutdown",
					"error", err,
					"duration_ms", duration.Milliseconds(),
				)
				return nil
			}
			w.handleJobFailure(jobCtx, job, jobType, err, duration, reservation)
			return &jobFailedError{err: fmt.Errorf("velocity/queue: job failed: %w", err)}
		}
		// Success: ack first, then run side effects only if we still
		// own the lease. A stale worker whose lease was reclaimed must
		// NOT bump batch counters or fire JobProcessed; the new owner
		// will do that when it succeeds. Doing side effects before the
		// fenced ack would double-count batches and double-emit events
		// for every slow-handler / lease-loss combination.
		owned := w.ackReservation(log, reservation)
		// removeAttempts is local cache cleanup, safe to do regardless
		// of ownership. The key is derived once here and skipped entirely
		// (nil) when the reservation already carries the persisted attempt
		// count; see attemptKey.
		w.removeAttempts(w.attemptKey(job, reservation))
		if !owned {
			return nil
		}
		if bj, ok := job.(Batchable); ok {
			if batch, found := FindBatch(bj.GetBatchID()); found {
				batch.recordSuccess(jobCtx)
			}
		}
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
		w.drainHandler(log, done)

		if w.ctx.Err() != nil {
			// Worker shutdown, not a real per-job timeout. Same reasoning
			// as the err-branch above: do not call handleJobFailure, do
			// not retry, do not mark Failed. Leave the row reserved; the
			// next worker reclaims it via the retryAfter predicate.
			log.Info("Job aborted by worker shutdown",
				"duration_ms", duration.Milliseconds(),
			)
			return nil
		}
		timeoutErr := fmt.Errorf("velocity/queue: job timed out")
		w.handleJobFailure(jobCtx, job, jobType, timeoutErr, duration, reservation)
		return &jobFailedError{err: timeoutErr}
	}
}

// ackReservation deletes the leased row after handler success on
// reservation-capable drivers. Returns true ONLY when the cleanup
// write completed successfully (AckCtx returned nil) and the caller is
// therefore the sole, durable owner of the result. The caller MUST gate
// success-side effects (batch counters, JobProcessed event) on the
// returned bool.
//
// Returns false on every error path:
//   - ErrLeaseLost: another worker reclaimed the row and will run side
//     effects; logged at WARN.
//   - Transient backend error (connection blip, pool exhausted,
//     deadlock-retry exhausted): the row stays reserved and will
//     redeliver via retryAfter; logged at WARN so operators see the
//     failure. The next attempt will either succeed (side effects fire
//     once) or also fail (no double-recording). Running side effects on
//     this attempt would double-count once the row redelivers.
//   - Unknown / wrapped: logged at ERROR; same skip-side-effects rule.
//
// Returns true for the zero-token case (no reservation applies, e.g.
// memory/redis drivers) and for non-reservation drivers, because the
// caller is the sole executor and there is no second worker to
// double-record.
//
// Uses a fresh background ctx with a short timeout so the ack survives
// jobCtx cancellation (e.g. when the handler completed just as the per-
// job timeout fires) and so a slow driver cannot hold shutdown open
// past its deadline.
func (w *Worker) ackReservation(log contract.Logger, token ReservationToken) bool {
	if token.IsZero() {
		// No lease to confirm: caller is the sole executor (memory /
		// redis driver, or a job sourced outside the reservation path).
		// Side effects must fire.
		return true
	}
	rd, ok := w.queue.(ReservationDriver)
	if !ok {
		return true
	}
	ackCtx, cancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cancel()
	switch err := rd.AckCtx(ackCtx, token); {
	case err == nil:
		return true
	case errors.Is(err, ErrLeaseLost):
		log.Warn("Lease lost before ack; the new owner will record success",
			"token", token.ID,
		)
		return false
	default:
		// Transient or unknown backend error. Do NOT run side effects:
		// the row is still reserved and will redeliver after the lease
		// expires, and the new attempt will record success exactly
		// once. Firing batch counters / JobProcessed here would
		// double-count once the redelivery succeeds.
		log.Warn("Ack failed; lease will expire and row will redeliver",
			"token", token.ID,
			"error", err,
		)
		return false
	}
}

// drainHandler waits for the detached handler goroutine to write to done
// after jobCtx fired. Bounded by defaultHandlerKillCeiling so a misbehaving
// handler that ignores ctx cannot hang Stop() forever; in that case we log
// a warning and let the goroutine leak.
func (w *Worker) drainHandler(log contract.Logger, done <-chan error) {
	select {
	case <-done:
		// handler returned cooperatively
	case <-time.After(defaultHandlerKillCeiling):
		log.Warn("Handler goroutine did not return after ctx cancellation; leaking",
			"kill_ceiling_ms", defaultHandlerKillCeiling.Milliseconds(),
		)
	}
}

// retryCarrier returns a context detached from jobCtx (no deadline, no
// cancellation, no values) that carries only the trace a retried attempt
// continues: the job's trace with the producer's span, which is the failed
// attempt's parent, as the current span. An attempt that started a root
// span (no producer span) retries from an untraced context and starts a new
// root, as it does on reservation drivers.
func retryCarrier(jobCtx context.Context) context.Context {
	producerSpan := trace.GetParentID(jobCtx)
	if producerSpan == "" {
		return context.Background()
	}
	return trace.WithTrace(context.Background(), trace.GetTraceID(jobCtx), producerSpan)
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
	log := w.jobLogger(ctx, job, jobType)
	maxAttempts := w.maxRetries
	if ma, ok := job.(MaxAttempter); ok {
		maxAttempts = ma.MaxAttempts()
	}

	// Determine the attempt number for the MaxAttempts decision. Durable
	// drivers report the persisted, post-increment value on the token;
	// non-durable drivers fall through to the in-memory cache. Derive the
	// attempt-tracking key once here and thread it to attemptNumber and
	// failJob so the content hash runs at most once per job lifecycle (and
	// not at all when the reservation already carries Attempts).
	key := w.attemptKey(job, reservation)
	attempt := w.attemptNumber(key, reservation)

	// Check if the job opts out of retrying this specific error
	if rd, ok := job.(RetryDecider); ok {
		if !rd.ShouldRetry(err) {
			w.failJob(ctx, log, job, jobType, err, duration, attempt, maxAttempts, key, reservation)
			return
		}
	}

	// If we have retries remaining (attempt < maxAttempts means we haven't used all attempts)
	if attempt < maxAttempts {
		backoff := w.calculateBackoff(job, attempt)
		// Use a detached context with a short timeout for the requeue so
		// a slow driver (Redis partition, DB lock wait) cannot hold
		// shutdown open past its deadline. If the requeue exceeds the
		// timeout the job is marked failed: losing the retry is
		// preferable to hanging the shutdown path.
		//
		// IMPORTANT: the requeue mutation MUST run before any
		// side-effect (log "Retrying job", dispatchJobRetrying). A
		// stale lease that has been reclaimed by another worker will
		// get ErrLeaseLost back from ReleaseCtx; firing the retry
		// event first would double-emit JobRetrying for the same row.
		//
		// The detached context carries the producer's span (the parent of
		// the attempt that failed), so a re-pushed copy runs its next
		// attempt as a new span under the same parent, as a released row
		// does on reservation drivers.
		pushCtx, pushCancel := context.WithTimeout(retryCarrier(ctx), retryPushTimeout)
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
				// in charge of side effects.
				log.Warn("Lease lost before retry release; another worker owns the row")
				return
			}
			log.Error("Failed to re-queue job for retry", "error", requeueErr)
			w.failJob(ctx, log, job, jobType, err, duration, attempt, maxAttempts, key, reservation)
			return
		}
		// Requeue confirmed (or no lease to lose): now safe to fire
		// the retry-side log + event.
		log.Info("Retrying job",
			"attempt", attempt,
			"max_attempts", maxAttempts,
			"backoff_ms", backoff.Milliseconds(),
			"error", err,
		)
		dispatchJobRetrying(w.dispatchEvent, ctx, jobType, w.queueName, attempt, maxAttempts, err, backoff)
		return
	}

	w.failJob(ctx, log, job, jobType, err, duration, attempt, maxAttempts, key, reservation)
}

// attemptNumber returns the authoritative attempt count for MaxAttempts
// decisions. For reservation-capable drivers, the persisted column value
// (carried on token.Attempts) wins because it survives worker restarts;
// the in-memory cache is bumped for parity but its return value is
// ignored. For non-reservation drivers the in-memory counter is the only
// source available.
func (w *Worker) attemptNumber(key interface{}, token ReservationToken) int {
	if !token.IsZero() && token.Attempts > 0 {
		// Persisted column wins. key is nil on this path (attemptKey
		// skipped the hash): the in-memory counter is not authoritative
		// for reservation drivers and is never read here, so there is no
		// cache to bump.
		return token.Attempts
	}
	return w.incrementAttempts(key)
}

// attemptKey derives the attempt-tracking key for a job exactly once per
// failure/cleanup and is threaded to attemptNumber / removeAttempts /
// failJob so the content hash in jobKey runs at most once per job
// lifecycle. Returns nil when the reservation already carries the persisted
// attempt count: that value is authoritative for reservation drivers, so
// the in-memory cache (and therefore the hash) is unnecessary. A nil key
// makes incrementAttempts / removeAttempts no-op against the cache.
func (w *Worker) attemptKey(job Job, token ReservationToken) interface{} {
	if !token.IsZero() && token.Attempts > 0 {
		return nil
	}
	return w.jobKey(job)
}

// failJob permanently fails a job after exhausting retries.
//
// Cleanup-first, then side effects: the FailReservedCtx mutation runs
// before the batch counter increment and the JobFailed event dispatch.
// Side effects fire ONLY when the cleanup write returned nil. Every
// error path (ErrLeaseLost, transient backend failure, unknown) skips
// batch.recordFailure + dispatchJobFailed and returns; the row stays
// reserved and either succeeds or fails again on the next attempt.
// This prevents the double-recording trap where a transient DB error
// would let the failure events fire while the row redelivers and the
// new worker records its own outcome on top.
//
// The driver-side cleanup write MUST use a context with its own short
// timeout, detached from the per-job ctx's cancellation: when this is
// reached via the jobCtx-timeout branch in processJob, ctx is already
// context.DeadlineExceeded and any DB write bound to it returns the
// deadline error before touching the row. The row would then stay
// reserved (never moved to failed_jobs) until the lease expires,
// breaking the at-least-once-but-bounded contract. The cleanup context
// keeps ctx's values, so the job's Failed hook, which the driver runs
// under it, sees the job's trace.
//
// Event dispatch still uses ctx so trace ids and request-scoped values
// propagate; only the database mutation runs under the detached
// terminalCleanupTimeout budget.
//
// The failure is reported or logged exactly once: with an event dispatcher,
// queue.job.failed carries it to the dispatcher's failure-report bridge; with
// none, nothing would report it, so failJob logs the job's own error at
// error level through the job's logger (job_type, queue, job_id and the
// job's trace, see jobLogger) with the attempts, unless the job's Failed
// hook already reported it (FailureSelfReporter).
func (w *Worker) failJob(ctx context.Context, log contract.Logger, job Job, jobType string, err error, duration time.Duration, attempt, maxAttempts int, key interface{}, reservation ReservationToken) {
	// Cleanup attempt cache regardless of ownership; this is pure
	// per-worker state. key is the precomputed attempt-tracking key
	// threaded from handleJobFailure (nil when the reservation already
	// carries Attempts and no cache entry was ever stored).
	w.removeAttempts(key)

	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCleanupTimeout)
	defer cleanupCancel()

	// Reservation-capable drivers record + delete the row atomically;
	// other drivers fall back to the bare FailedCtx() path.
	if rd, ok := w.queue.(ReservationDriver); ok && !reservation.IsZero() {
		switch failErr := rd.FailReservedCtx(cleanupCtx, reservation, job, err, w.queueName); {
		case failErr == nil:
			// Ownership confirmed; safe to fire side effects below.
		case errors.Is(failErr, ErrFailedHookPanicked):
			// Ownership confirmed and the failure recorded; only the
			// job's Failed hook panicked. Log it and run the side
			// effects below as for a clean record, so the batch and the
			// queue.job.failed event still see this failure.
			log.Error("Job Failed hook panicked after the failure was recorded",
				"error", failErr,
			)
		case errors.Is(failErr, ErrLeaseLost):
			// Another worker reclaimed the row; the new owner is now
			// responsible for it. Log and stop -- do NOT bump batch
			// counters or fire JobFailed; the new owner will when its
			// own attempt terminates.
			log.Warn("Lease lost before terminal cleanup; the new owner will record failure")
			return
		default:
			// Transient or unknown backend failure. The row is still
			// reserved and will redeliver after the lease expires; the
			// next attempt either records success cleanly or fails
			// again. Running side effects here would double-count once
			// the redelivery completes -- the symmetric trap to the
			// ack path.
			log.Warn("FailReservedCtx errored; lease will expire and row will redeliver",
				"error", failErr,
			)
			return
		}
	} else {
		// Non-reservation driver (memory, redis): the row was deleted
		// at pop time, so there is no redelivery to double-count
		// against. Run side effects regardless of FailedCtx()'s outcome
		// so alerting pipelines still see the failure when the
		// failed_jobs sink itself is degraded.
		switch failErr := w.queue.FailedCtx(cleanupCtx, job, err, w.queueName); {
		case failErr == nil:
		case errors.Is(failErr, ErrFailedHookPanicked):
			log.Error("Job Failed hook panicked after the failure was recorded",
				"error", failErr,
			)
		default:
			log.Error("Failed to mark job as failed", "error", failErr)
		}
	}

	// Side effects below run only on confirmed-ownership paths above.
	if bj, ok := job.(Batchable); ok {
		if batch, found := FindBatch(bj.GetBatchID()); found {
			batch.recordFailure(ctx, err)
		}
	}
	failure := failureForEvent(job, err)
	dispatch := w.currentEventDispatcher()
	if dispatch == nil {
		if !contract.IsReported(failure) {
			log.Error("Job failed",
				"attempts", attempt,
				"error", err,
			)
		}
		return
	}
	dispatchJobFailed(func(ctx context.Context, event interface{}) { _ = dispatch(ctx, event) }, ctx, jobType, w.queueName, failure, duration)
}

// failureForEvent returns the error the queue.job.failed event carries for a job
// the driver has just failed: err marked reported (contract.MarkReported)
// when the driver's call ran the job's Failed hook and the hook reported
// err itself (see FailureSelfReporter), so the dispatcher's failure-report
// bridge skips it and the failure is reported once; err unchanged
// otherwise, so the bridge reports it.
func failureForEvent(job Job, err error) error {
	if sr, ok := job.(FailureSelfReporter); ok && sr.FailureReported() {
		return contract.MarkReported(err)
	}
	return err
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
	// Non-Identifiable jobs re-hydrate to a fresh pointer on every pop from a
	// delete-on-pop driver (redis): pointer identity changes each attempt, so
	// the in-process attempt counter would never advance and a perpetually
	// failing job would retry forever, never reaching failed_jobs. Derive a
	// stable content key from the marshaled job so repeated pops of the same
	// payload share one counter and MaxAttempts is enforced. Two distinct jobs
	// with byte-identical content share a counter, which only ever fails one
	// slightly early; that is acceptable next to unbounded retries.
	if b, err := json.Marshal(job); err == nil {
		sum := sha256.Sum256(b)
		return string(sum[:])
	}
	// Last resort: pointer identity (memory-driver semantics).
	return job
}

// incrementAttempts atomically increments and returns the attempt count
// for the given precomputed key (see attemptKey / jobKey). A nil key means
// the caller is on the reservation-authoritative path where the persisted
// column wins and the in-memory cache is intentionally skipped; it returns
// 0 and the value is discarded.
func (w *Worker) incrementAttempts(key interface{}) int {
	if key == nil {
		return 0
	}
	val, _ := w.attempts.LoadOrStore(key, new(int32))
	counter := val.(*int32)
	return int(atomic.AddInt32(counter, 1))
}

// removeAttempts cleans up attempt tracking for the given precomputed key.
// A nil key is a no-op: nothing was stored on the reservation-authoritative
// path.
func (w *Worker) removeAttempts(key interface{}) {
	if key == nil {
		return
	}
	w.attempts.Delete(key)
}
