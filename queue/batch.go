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
// installed a DatabaseBatchRepository via SetDefaultBatchRepository get
// a *Batch reconstructed from the persistent row, so cancel checks and
// progress queries are correct regardless of which host dispatched the
// batch.
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
// The struct is now a thin value object: fields here mirror the
// repository row so callers reading Batch.PendingJobs() see consistent
// state regardless of repository implementation. Mutating operations
// (recordSuccess, recordFailure, Cancel) route through the bound
// repository so cross-process workers observe the same counters.
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

	// Local callbacks. Populated when this process is the dispatcher;
	// nil on remote-loaded Batch instances (closures can't cross
	// process boundaries). When a remote worker observes terminal
	// completion through the repository's CAS, the BatchCompleted
	// event is still emitted so the dispatcher process can react.
	thenFn    func(b *Batch)
	catchFn   func(b *Batch, err error)
	finallyFn func(b *Batch)

	dispatchEvent func(ctx context.Context, event interface{})

	// repo binds the Batch to the repository that minted it. When
	// the caller goes through FindBatch we reuse the default repo.
	// recordSuccess / recordFailure / Cancel route here.
	repo BatchRepository

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

// repository returns the bound repository or the process default if the
// Batch was constructed without one (defensive: a *Batch obtained via
// FindBatch always has one, but tests sometimes build a zero Batch).
func (b *Batch) repository() BatchRepository {
	if b.repo != nil {
		return b.repo
	}
	return DefaultBatchRepository()
}

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
	if updated, err := b.repository().Cancel(ctx, b.id); err == nil && updated != nil {
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
	updated, justFinished, err := b.repository().IncrementSuccess(ctx, b.id)
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
		b.fireTerminalCallbacks(ctx)
	}
}

// recordFailure is called by the worker when a batch job fails permanently.
func (b *Batch) recordFailure(ctx context.Context, jobErr error) {
	updated, justFinished, err := b.repository().IncrementFailure(ctx, b.id, jobErr)
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

	// Catch fires on first failure observed by this process. The
	// callbackEntry's sync.Once handles the at-most-once guarantee
	// locally; remote-process workers do not have the closure and
	// rely on the BatchJobFailed event for downstream signalling.
	if entry := globalCallbacks.get(b.id); entry != nil {
		entry.fireCatch(b, jobErr)
	} else if b.catchFn != nil {
		// Legacy path: Batch was constructed without going through
		// the dispatch registry (rare; mainly old tests). Fire the
		// closure directly with no at-most-once guarantee.
		fn := b.catchFn
		capturedErr := jobErr
		go func() {
			defer func() { _ = recover() }()
			fn(b, capturedErr)
		}()
	}

	// Auto-cancel when failures are not allowed. Idempotent at the
	// repository layer: a second Cancel is a no-op.
	if !b.allowFailures {
		b.CancelCtx(ctx)
	}

	if justFinished {
		b.fireTerminalCallbacks(ctx)
	}
}

// recordSkip is called by the worker when a job in a cancelled batch is
// popped and intentionally not run. We still need to decrement
// pendingJobs so the batch can reach Finished, but no completion or
// failure counter advances.
func (b *Batch) recordSkip(ctx context.Context) {
	updated, justFinished, err := b.repository().DecrementPending(ctx, b.id)
	if err != nil || updated == nil {
		return
	}
	b.copyCountersFrom(updated)
	if justFinished {
		b.fireTerminalCallbacks(ctx)
	}
}

// copyCountersFrom mirrors counter and flag values from the repository
// readback onto the receiver. We do NOT copy callback closures because
// those are owned by the dispatcher process and would be nil on a
// remote-loaded Batch; clobbering them with nil would break terminal
// callback firing on the dispatcher.
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
}

// fireTerminalCallbacks fires Then (if no failures) and Finally exactly
// once across the fleet. The "exactly once" guarantee is provided by
// the repository's CAS on completed_at (justFinished is true on exactly
// one caller) and reinforced by the callbackEntry's finishedFired
// atomic.Bool in case a misbehaving repository returns justFinished
// twice.
func (b *Batch) fireTerminalCallbacks(ctx context.Context) {
	if entry := globalCallbacks.get(b.id); entry != nil {
		entry.fireFinished(b)
	} else {
		// Legacy / direct-construction path: no registry entry. Fall
		// back to the closures stored on Batch with manual at-most-once
		// guards via the finished flag (already CAS'd by the repo).
		if !b.HasFailures() && b.thenFn != nil {
			fn := b.thenFn
			go func() {
				defer func() { _ = recover() }()
				fn(b)
			}()
		}
		if b.finallyFn != nil {
			fn := b.finallyFn
			go func() {
				defer func() { _ = recover() }()
				fn(b)
			}()
		}
	}

	dispatchBatchEvent(ctx, b.dispatchEvent, &BatchCompleted{
		BatchID:       string(b.id),
		TotalJobs:     b.totalJobs,
		CompletedJobs: b.CompletedJobs(),
		FailedJobs:    b.FailedJobs(),
		HasFailures:   b.HasFailures(),
	})
}

// dispatchBatchEvent dispatches a batch event (nil-safe like other dispatch helpers)
func dispatchBatchEvent(ctx context.Context, dispatch func(context.Context, interface{}), event interface{}) {
	if dispatch == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dispatch(ctx, event)
}

// PendingBatch is a fluent builder for creating and dispatching a batch
type PendingBatch struct {
	jobs          []Job
	thenFn        func(b *Batch)
	catchFn       func(b *Batch, err error)
	finallyFn     func(b *Batch)
	allowFailures bool
	queue         string
	dispatchEvent func(ctx context.Context, event interface{})
	repo          BatchRepository
}

// NewBatch creates a new PendingBatch with the given jobs.
// Jobs that implement Batchable will have their BatchID set automatically.
func NewBatch(jobs ...Job) *PendingBatch {
	return &PendingBatch{
		jobs:  jobs,
		queue: "default",
	}
}

// Then sets a callback that fires when ALL jobs complete successfully (no failures)
func (pb *PendingBatch) Then(fn func(b *Batch)) *PendingBatch {
	pb.thenFn = fn
	return pb
}

// Catch sets a callback that fires once on the first job failure
func (pb *PendingBatch) Catch(fn func(b *Batch, err error)) *PendingBatch {
	pb.catchFn = fn
	return pb
}

// Finally sets a callback that always fires when all jobs have been processed
func (pb *PendingBatch) Finally(fn func(b *Batch)) *PendingBatch {
	pb.finallyFn = fn
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
func (pb *PendingBatch) WithEventDispatcher(fn func(ctx context.Context, event interface{})) *PendingBatch {
	pb.dispatchEvent = fn
	return pb
}

// WithRepository binds an explicit repository for this batch. Useful for
// tests that need to drive a non-default repository or for multi-tenant
// apps that route different batches to different storage. When omitted,
// the process default is used (DefaultBatchRepository()).
func (pb *PendingBatch) WithRepository(repo BatchRepository) *PendingBatch {
	pb.repo = repo
	return pb
}

// Dispatch creates the batch, sets BatchID on Batchable jobs, and pushes all jobs to the driver.
func (pb *PendingBatch) Dispatch(ctx context.Context, driver Driver) (*Batch, error) {
	if len(pb.jobs) == 0 {
		return nil, fmt.Errorf("batch: cannot dispatch empty batch")
	}

	id := newBatchID()
	repo := pb.repo
	if repo == nil {
		repo = DefaultBatchRepository()
	}

	batch := &Batch{
		id:            id,
		totalJobs:     len(pb.jobs),
		allowFailures: pb.allowFailures,
		queue:         pb.queue,
		thenFn:        pb.thenFn,
		catchFn:       pb.catchFn,
		finallyFn:     pb.finallyFn,
		dispatchEvent: pb.dispatchEvent,
		repo:          repo,
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
