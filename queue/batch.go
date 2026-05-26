package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// BatchID is a unique identifier for a batch.
//
// Prior to C-03 these were sequential counters keyed off a process-local
// store (`batch_1`, `batch_2`, ...). Two producer processes in a multi-
// host deployment minted the same counter values, then collided when
// their workers fanned out across a shared queue backend. Switching to
// UUIDv7 makes the ID globally unique and naturally sortable by
// creation time so DB indices behave well under range scans.
type BatchID string

// Batchable is an optional interface that jobs can implement to participate in batches.
// This follows the same pattern as MaxAttempter, OnQueuer, etc.
type Batchable interface {
	GetBatchID() BatchID
	SetBatchID(id BatchID)
}

// newBatchID mints a fresh, collision-safe BatchID.
//
// UUIDv7 is preferred because the leading 48 bits are a Unix-ms
// timestamp, which keeps DB index pages roughly in insertion order
// (matters for the job_batches table where reads are dominated by
// "find recently-finished batches"). If uuid.NewV7 fails (the random
// pool is exhausted, which is effectively impossible on Linux but
// theoretically possible elsewhere) we fall back to uuid.NewRandom
// rather than panicking; either form is collision-safe across
// producer processes.
func newBatchID() BatchID {
	id, err := uuid.NewV7()
	if err != nil {
		// NewRandom uses crypto/rand and effectively cannot fail on any
		// supported platform; if both fail we are in deep trouble and
		// surfacing a non-unique ID is worse than panicking, so we let
		// the caller handle the unlikely error path explicitly.
		id = uuid.New()
	}
	return BatchID("batch_" + id.String())
}

// FindBatch looks up a batch by ID through the process-wide repository.
//
// Callers in single-host deployments get the in-memory repository (the
// historical behaviour). Callers in multi-host deployments that have
// installed a DatabaseBatchRepository via SetDefaultBatchRepository (or
// via the framework's auto-install) get a *Batch reconstructed from the
// persistent row, so cancel checks and progress queries are correct
// regardless of which host dispatched the batch.
//
// The boolean second return preserves the legacy signature so worker
// code does not need to change. Storage errors are swallowed (returned
// as (nil, false)) to mirror the original map-lookup semantics; callers
// that need to distinguish "not found" from "DB error" should call
// DefaultBatchRepository().Find directly.
func FindBatch(id BatchID) (*Batch, bool) {
	b, err := DefaultBatchRepository().Find(context.Background(), id)
	if err != nil || b == nil {
		return nil, false
	}
	return b, true
}

// Batch tracks the state of a group of jobs.
//
// The struct is a thin value object: fields here mirror the repository
// row so callers reading Batch.PendingJobs() see consistent state
// regardless of repository implementation. Mutating operations
// (recordSuccess, recordFailure, Cancel) route through the process-wide
// repository so cross-process workers observe the same counters.
//
// Callback delivery across processes is via the persisted thenName /
// catchName / finallyName fields plus the BatchCallbackJob mechanism:
// see queue/batch_callback.go for the full path. Closures (thenFn etc.)
// remain as a convenience for in-process callers and fire ONLY in the
// dispatcher process via the local callbackEntry registry.
type Batch struct {
	id            BatchID
	totalJobs     int
	pendingJobs   atomic.Int32
	completedJobs atomic.Int32
	failedJobs    atomic.Int32
	allowFailures bool
	cancelled     atomic.Bool
	finished      atomic.Bool
	finishedAt    time.Time
	queue         string
	lastError     string

	// Named callbacks. Populated when the dispatcher used OnComplete /
	// OnFailed / OnFinally with a name registered via
	// RegisterBatchCallback. Persisted on the job_batches row so any
	// host with a worker that runs BatchCallbackJob can dispatch them.
	thenName    string
	catchName   string
	finallyName string

	// dispatched flags track whether the corresponding callback job has
	// been successfully PushCtx'd onto the queue. The terminal CAS path
	// attempts the enqueue inline; if PushCtx fails the flag stays
	// false and the reaper goroutine on DatabaseBatchRepository retries
	// every 15s. The flags are atomic.Bool so the reaper and the
	// terminal path can race without dropping notifications.
	thenDispatched    atomic.Bool
	catchDispatched   atomic.Bool
	finallyDispatched atomic.Bool

	// Local closure callbacks. Populated when the dispatcher used the
	// Then/Catch/Finally func variants. nil on remote-loaded Batch
	// instances (closures don't cross processes). When set, the
	// dispatcher process fires them locally on terminal completion.
	thenFn    func(b *Batch)
	catchFn   func(b *Batch, err error)
	finallyFn func(b *Batch)

	dispatchEvent func(ctx context.Context, event interface{})

	mu sync.Mutex // protects finishedAt and lastError
}

// ID returns the batch identifier
func (b *Batch) ID() BatchID { return b.id }

// TotalJobs returns the total number of jobs in the batch
func (b *Batch) TotalJobs() int { return b.totalJobs }

// PendingJobs returns the number of jobs still pending
func (b *Batch) PendingJobs() int { return int(b.pendingJobs.Load()) }

// CompletedJobs returns the number of successfully completed jobs
func (b *Batch) CompletedJobs() int { return int(b.completedJobs.Load()) }

// FailedJobs returns the number of failed jobs
func (b *Batch) FailedJobs() int { return int(b.failedJobs.Load()) }

// ProcessedJobs returns the total number of processed jobs (completed + failed)
func (b *Batch) ProcessedJobs() int { return b.CompletedJobs() + b.FailedJobs() }

// Progress returns the batch progress as a percentage (0-100)
func (b *Batch) Progress() float64 {
	if b.totalJobs == 0 {
		return 100.0
	}
	return float64(b.ProcessedJobs()) / float64(b.totalJobs) * 100.0
}

// Finished returns whether the batch has completed (all jobs processed)
func (b *Batch) Finished() bool { return b.finished.Load() }

// Cancelled returns whether the batch has been cancelled
func (b *Batch) Cancelled() bool { return b.cancelled.Load() }

// HasFailures returns whether any jobs in the batch have failed
func (b *Batch) HasFailures() bool { return b.failedJobs.Load() > 0 }

// AllowsFailures returns whether the batch is configured to allow failures
func (b *Batch) AllowsFailures() bool { return b.allowFailures }

// ThenCallbackName returns the persisted Then callback name (or empty
// when none). Useful for inspection by listeners that prefer subscribing
// over registering a callback handler.
func (b *Batch) ThenCallbackName() string { return b.thenName }

// CatchCallbackName returns the persisted Catch callback name.
func (b *Batch) CatchCallbackName() string { return b.catchName }

// FinallyCallbackName returns the persisted Finally callback name.
func (b *Batch) FinallyCallbackName() string { return b.finallyName }

// Cancel cancels the batch, preventing remaining jobs from being processed.
// Callers that have a real ctx in scope should prefer CancelCtx.
func (b *Batch) Cancel() {
	b.CancelCtx(context.Background())
}

// CancelCtx is the context-aware variant of Cancel. The supplied ctx is
// propagated to the repository write and to listeners observing the
// BatchCancelled event.
func (b *Batch) CancelCtx(ctx context.Context) {
	// Cache the previous cancelled state so we can fire the event
	// exactly once even when two callers race Cancel.
	if !b.cancelled.CompareAndSwap(false, true) {
		return
	}
	// Persist the cancellation via the repository so cross-process
	// workers see it.
	if updated, err := DefaultBatchRepository().Cancel(ctx, b.id); err == nil && updated != nil {
		// Mirror DB-side counter values into the local Batch so a
		// subsequent FailedJobs() reflects whatever else changed
		// between our last read and the cancel.
		b.failedJobs.Store(updated.failedJobs.Load())
		b.completedJobs.Store(updated.completedJobs.Load())
		b.pendingJobs.Store(updated.pendingJobs.Load())
	}
	dispatchBatchEvent(ctx, b.dispatchEvent, &BatchCancelled{
		BatchID:    string(b.id),
		FailedJobs: b.FailedJobs(),
	})
}

// recordSuccess is called by the worker when a batch job completes successfully.
// All counter mutation is delegated to the repository so the DB-backed
// implementation can run an atomic UPDATE; the in-memory implementation
// updates the same atomic.Int32 fields the caller already reads.
func (b *Batch) recordSuccess(ctx context.Context) {
	updated, justFinished, err := DefaultBatchRepository().IncrementSuccess(ctx, b.id)
	if err != nil || updated == nil {
		return
	}
	// Mirror the persisted counters onto the receiver so callers that
	// keep a reference (the original dispatcher) see fresh values
	// without re-issuing a Find.
	b.copyCountersFrom(updated)

	dispatchBatchEvent(ctx, b.dispatchEvent, &BatchJobCompleted{
		BatchID:       string(b.id),
		CompletedJobs: int(updated.completedJobs.Load()),
		TotalJobs:     b.totalJobs,
		Progress:      b.Progress(),
	})

	if justFinished {
		b.fireTerminalCallbacks(ctx, updated)
	}
}

// recordFailure is called by the worker when a batch job fails permanently.
func (b *Batch) recordFailure(ctx context.Context, jobErr error) {
	updated, justFinished, err := DefaultBatchRepository().IncrementFailure(ctx, b.id, jobErr)
	if err != nil || updated == nil {
		return
	}
	b.copyCountersFrom(updated)

	dispatchBatchEvent(ctx, b.dispatchEvent, &BatchJobFailed{
		BatchID:    string(b.id),
		FailedJobs: int(updated.failedJobs.Load()),
		TotalJobs:  b.totalJobs,
		Error:      jobErr.Error(),
	})

	// Catch fires on the first failure observed by ANY host. In-process
	// closures fire on the dispatcher via the callbackEntry's sync.Once;
	// for cross-process delivery we enqueue a BatchCallbackJob so any
	// worker can run the registered handler. Both paths are at-most-once
	// per process: the local sync.Once guards the closure and the
	// queue's job-uniqueness (via JobID) guards the named handler.
	if entry := globalCallbacks.get(b.id); entry != nil {
		entry.fireCatch(b, jobErr)
	}
	if name := b.useCatchName(updated); name != "" {
		dispatchBatchCallbackJob(ctx, name, CallbackCatch, b.id, jobErr.Error())
	}

	// Auto-cancel when failures are not allowed. Idempotent at the
	// repository layer: a second Cancel is a no-op.
	if !b.allowFailures {
		b.CancelCtx(ctx)
	}

	if justFinished {
		b.fireTerminalCallbacks(ctx, updated)
	}
}

// recordSkip is called by the worker when a job in a cancelled batch is
// popped and intentionally not run. We still need to decrement
// pendingJobs so the batch can reach Finished, but no completion or
// failure counter advances.
func (b *Batch) recordSkip(ctx context.Context) {
	updated, justFinished, err := DefaultBatchRepository().DecrementPending(ctx, b.id)
	if err != nil || updated == nil {
		return
	}
	b.copyCountersFrom(updated)
	if justFinished {
		b.fireTerminalCallbacks(ctx, updated)
	}
}

// useCatchName picks the catch callback name from the most-recent
// repository readback (preferred, in case the repo learned about a
// previously-persisted name) and falls back to the in-memory copy.
func (b *Batch) useCatchName(updated *Batch) string {
	if updated != nil && updated.catchName != "" {
		return updated.catchName
	}
	return b.catchName
}

func (b *Batch) useThenName(updated *Batch) string {
	if updated != nil && updated.thenName != "" {
		return updated.thenName
	}
	return b.thenName
}

func (b *Batch) useFinallyName(updated *Batch) string {
	if updated != nil && updated.finallyName != "" {
		return updated.finallyName
	}
	return b.finallyName
}

// copyCountersFrom mirrors counter and flag values from the repository
// readback onto the receiver. We do NOT copy closures or callback names
// because those are owned by the dispatcher process (closures) or by
// the repository row already loaded into the receiver (names); clobbering
// them on the dispatcher would break terminal callback firing.
func (b *Batch) copyCountersFrom(src *Batch) {
	if src == nil || src == b {
		return
	}
	b.pendingJobs.Store(src.pendingJobs.Load())
	b.completedJobs.Store(src.completedJobs.Load())
	b.failedJobs.Store(src.failedJobs.Load())
	if src.cancelled.Load() {
		b.cancelled.Store(true)
	}
	if src.finished.Load() {
		b.finished.Store(true)
		b.mu.Lock()
		if b.finishedAt.IsZero() {
			b.finishedAt = src.finishedAt
		}
		if src.lastError != "" {
			b.lastError = src.lastError
		}
		b.mu.Unlock()
	}
	// Names: copy if we don't already have one (the receiver was
	// constructed without going through Save, e.g. on a remote worker
	// that just called FindBatch).
	if b.thenName == "" && src.thenName != "" {
		b.thenName = src.thenName
	}
	if b.catchName == "" && src.catchName != "" {
		b.catchName = src.catchName
	}
	if b.finallyName == "" && src.finallyName != "" {
		b.finallyName = src.finallyName
	}
}

// fireTerminalCallbacks is invoked when the repository's CAS confirms
// the batch transitioned to its terminal state (justFinished=true on
// the caller). It fires local closures and enqueues named callbacks
// so any worker can execute them, regardless of which host completed
// the last job.
//
// updated is the repository readback that includes any callback names
// persisted on the row; if non-nil it takes precedence over the
// receiver's cached names (used by remote workers that just loaded the
// batch).
func (b *Batch) fireTerminalCallbacks(ctx context.Context, updated *Batch) {
	// Local closure path: only the dispatcher process has these. The
	// entry's finishedFired atomic.Bool gates against double-fire even
	// when the CAS races with a duplicate observation.
	if entry := globalCallbacks.get(b.id); entry != nil {
		entry.fireFinished(b)
	}

	// Cross-process named-callback path. Enqueue a BatchCallbackJob
	// for each non-empty callback name; the worker that pops it looks
	// up the registered handler and runs it. We always enqueue on
	// terminal completion regardless of whether closures fired,
	// because the dispatcher process may not have the named handler
	// registered (a different service is meant to handle it).
	if !b.HasFailures() {
		if name := b.useThenName(updated); name != "" {
			dispatchBatchCallbackJob(ctx, name, CallbackThen, b.id, "")
		}
	}
	if name := b.useFinallyName(updated); name != "" {
		errMsg := ""
		if updated != nil {
			errMsg = updated.lastError
		}
		dispatchBatchCallbackJob(ctx, name, CallbackFinally, b.id, errMsg)
	}

	dispatchBatchEvent(ctx, b.dispatchEvent, &BatchCompleted{
		BatchID:       string(b.id),
		TotalJobs:     b.totalJobs,
		CompletedJobs: b.CompletedJobs(),
		FailedJobs:    b.FailedJobs(),
		HasFailures:   b.HasFailures(),
	})
}

// dispatchBatchEvent dispatches a batch event through the batch's local
// dispatcher when one is bound and ALWAYS through the process-wide
// events dispatcher when one is wired via SetGlobalEventDispatcher.
//
// Pre-C-03-fb2 this helper silently dropped events when no local
// dispatcher was bound, which meant a remote worker observing terminal
// completion published nothing - so the dispatcher process on another
// host never saw BatchCompleted. We now also route through the
// process-wide dispatcher so subscribers (events.Listen) get the
// notification regardless of which host fired the CAS.
//
// dispatch (the batch's local dispatcher) is allowed to be nil: it is
// only populated when the dispatcher process used WithEventDispatcher.
// The global dispatcher is what makes cross-process subscriptions work.
func dispatchBatchEvent(ctx context.Context, dispatch func(context.Context, interface{}), event interface{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dispatch != nil {
		dispatch(ctx, event)
	}
	if g := globalEventDispatcher(); g != nil {
		_ = g(ctx, event)
	}
}

// globalDispatcherFn is the type the framework wires when calling
// SetGlobalEventDispatcher.
type globalDispatcherFn func(ctx context.Context, event interface{}) error

var globalEventDispatcherSlot atomic.Pointer[globalDispatcherFn]

// SetGlobalEventDispatcher installs a process-wide event dispatcher
// that the batch lifecycle helpers will invoke for every batch event
// (Created / JobCompleted / JobFailed / Completed / Cancelled).
//
// The framework's wireInstanceEvents calls this with the App's events
// dispatcher. The hook is exposed publicly so test harnesses (and
// embedded apps that bring their own dispatcher) can wire it directly.
//
// Pass nil to clear.
func SetGlobalEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	if fn == nil {
		globalEventDispatcherSlot.Store(nil)
		return
	}
	wrapped := globalDispatcherFn(fn)
	globalEventDispatcherSlot.Store(&wrapped)
}

func globalEventDispatcher() globalDispatcherFn {
	p := globalEventDispatcherSlot.Load()
	if p == nil {
		return nil
	}
	return *p
}

// PendingBatch is a fluent builder for creating and dispatching a batch
type PendingBatch struct {
	jobs          []Job
	thenFn        func(b *Batch)
	catchFn       func(b *Batch, err error)
	finallyFn     func(b *Batch)
	thenName      string
	catchName     string
	finallyName   string
	allowFailures bool
	queue         string
	dispatchEvent func(ctx context.Context, event interface{})
}

// NewBatch creates a new PendingBatch with the given jobs.
// Jobs that implement Batchable will have their BatchID set automatically.
func NewBatch(jobs ...Job) *PendingBatch {
	return &PendingBatch{
		jobs:  jobs,
		queue: "default",
	}
}

// Then sets a closure that fires when ALL jobs complete successfully (no failures).
//
// Closures fire ONLY in the dispatching process. For cross-process
// delivery (multi-host worker fleets), use OnComplete with a name
// registered via RegisterBatchCallback.
func (pb *PendingBatch) Then(fn func(b *Batch)) *PendingBatch {
	pb.thenFn = fn
	return pb
}

// Catch sets a closure that fires on the first job failure. Closures
// fire only in-process; use OnFailed for cross-process delivery.
func (pb *PendingBatch) Catch(fn func(b *Batch, err error)) *PendingBatch {
	pb.catchFn = fn
	return pb
}

// Finally sets a closure that fires when the batch reaches its terminal
// state. Closures fire only in-process; use OnFinally for cross-process
// delivery.
func (pb *PendingBatch) Finally(fn func(b *Batch)) *PendingBatch {
	pb.finallyFn = fn
	return pb
}

// OnComplete binds a NAMED callback handler that fires when every job
// in the batch completes without failure. Unlike Then, OnComplete is
// safe across processes: the name is persisted to the job_batches row,
// and the terminal completion CAS enqueues a BatchCallbackJob that any
// worker can execute.
//
// The supplied name must be registered via RegisterBatchCallback on
// every worker host that might run callback jobs.
func (pb *PendingBatch) OnComplete(name string) *PendingBatch {
	pb.thenName = name
	return pb
}

// OnFailed binds a NAMED Catch handler (failure path).
// Register via RegisterBatchFailureCallback.
func (pb *PendingBatch) OnFailed(name string) *PendingBatch {
	pb.catchName = name
	return pb
}

// OnFinally binds a NAMED Finally handler.
// Register via RegisterBatchCallback.
func (pb *PendingBatch) OnFinally(name string) *PendingBatch {
	pb.finallyName = name
	return pb
}

// AllowFailures configures the batch to continue processing even when jobs fail
func (pb *PendingBatch) AllowFailures() *PendingBatch {
	pb.allowFailures = true
	return pb
}

// OnQueue sets the queue name for all jobs in the batch
func (pb *PendingBatch) OnQueue(queue string) *PendingBatch {
	pb.queue = queue
	return pb
}

// WithEventDispatcher sets the event dispatcher for the batch. The fn
// receives a context.Context so listeners observe per-job scoped values.
//
// This sets the batch's LOCAL dispatcher (per-batch listener). Apps that
// want cross-process notification should also call
// queue.SetGlobalEventDispatcher (typically wired by the framework's
// bootstrap).
func (pb *PendingBatch) WithEventDispatcher(fn func(ctx context.Context, event interface{})) *PendingBatch {
	pb.dispatchEvent = fn
	return pb
}

// Dispatch creates the batch, sets BatchID on Batchable jobs, and pushes all jobs to the driver.
func (pb *PendingBatch) Dispatch(ctx context.Context, driver Driver) (*Batch, error) {
	if len(pb.jobs) == 0 {
		return nil, fmt.Errorf("batch: cannot dispatch empty batch")
	}

	id := newBatchID()
	repo := DefaultBatchRepository()

	batch := &Batch{
		id:            id,
		totalJobs:     len(pb.jobs),
		allowFailures: pb.allowFailures,
		queue:         pb.queue,
		thenFn:        pb.thenFn,
		catchFn:       pb.catchFn,
		finallyFn:     pb.finallyFn,
		thenName:      pb.thenName,
		catchName:     pb.catchName,
		finallyName:   pb.finallyName,
		dispatchEvent: pb.dispatchEvent,
	}
	batch.pendingJobs.Store(int32(len(pb.jobs)))

	// Register the local callbacks so any process observing a terminal
	// transition for this ID can fire them. The registry uses sync.Once
	// for at-most-once Catch and atomic.Bool for at-most-once Then/Finally.
	globalCallbacks.register(id, &callbackEntry{
		thenFn:        pb.thenFn,
		catchFn:       pb.catchFn,
		finallyFn:     pb.finallyFn,
		dispatchEvent: pb.dispatchEvent,
		allowFailures: pb.allowFailures,
		queue:         pb.queue,
		totalJobs:     len(pb.jobs),
	})

	if err := repo.Save(ctx, batch); err != nil {
		globalCallbacks.remove(id)
		return nil, fmt.Errorf("batch: failed to save batch: %w", err)
	}

	// Set BatchID on all Batchable jobs and push them
	pushed := 0
	for _, job := range pb.jobs {
		if bj, ok := job.(Batchable); ok {
			bj.SetBatchID(id)
		}
		queueName := pb.queue
		if oq, ok := job.(OnQueuer); ok {
			queueName = oq.OnQueue()
		}
		if err := driver.PushCtx(ctx, job, queueName); err != nil {
			// Adjust pendingJobs to reflect only the jobs that were actually pushed,
			// then cancel the batch so it can still reach Finished state.
			unpushed := len(pb.jobs) - pushed
			for i := 0; i < unpushed; i++ {
				_, _, _ = repo.DecrementPending(ctx, batch.id)
			}
			// Reflect the repository's view onto the local Batch for callers.
			if refreshed, ferr := repo.Find(ctx, batch.id); ferr == nil && refreshed != nil {
				batch.copyCountersFrom(refreshed)
			}
			batch.CancelCtx(ctx)
			return batch, fmt.Errorf("batch: failed to push job %d/%d: %w", pushed+1, len(pb.jobs), err)
		}
		pushed++
	}

	dispatchBatchEvent(ctx, pb.dispatchEvent, &BatchCreated{
		BatchID:   string(id),
		TotalJobs: len(pb.jobs),
		Queue:     pb.queue,
	})

	return batch, nil
}
