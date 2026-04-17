package queue

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
)

// BatchID is a unique identifier for a batch
type BatchID string

// Batchable is an optional interface that jobs can implement to participate in batches.
// This follows the same pattern as MaxAttempter, OnQueuer, etc.
type Batchable interface {
	GetBatchID() BatchID
	SetBatchID(id BatchID)
}

// batchStore holds all active batches (package-level, like jobStore in job_wrapper.go)
var batchStore = newBatchStore()

type batchStoreMap struct {
	mu      sync.RWMutex
	batches map[BatchID]*Batch
	seq     uint64
	stop    chan struct{}
}

func newBatchStore() *batchStoreMap {
	s := &batchStoreMap{
		batches: make(map[BatchID]*Batch),
		stop:    make(chan struct{}),
	}
	async.Go(func() { s.periodicCleanup() })
	return s
}

// periodicCleanup removes finished batches older than 1 hour (same pattern as jobStore).
// Exits when the stop channel is closed.
func (s *batchStoreMap) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.mu.Lock()
			cutoff := time.Now().Add(-1 * time.Hour)
			for id, b := range s.batches {
				if b.finished.Load() && b.finishedAt.Before(cutoff) {
					delete(s.batches, id)
				}
			}
			s.mu.Unlock()
		case <-s.stop:
			return
		}
	}
}

// close stops the periodic cleanup goroutine.
func (s *batchStoreMap) close() {
	select {
	case <-s.stop:
		// already closed
	default:
		close(s.stop)
	}
}

// reset clears all batches and resets the ID sequence. Used by tests.
func (s *batchStoreMap) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = make(map[BatchID]*Batch)
	s.seq = 0
}

func (s *batchStoreMap) store(b *Batch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches[b.id] = b
}

func (s *batchStoreMap) get(id BatchID) (*Batch, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.batches[id]
	return b, ok
}

func (s *batchStoreMap) nextID() BatchID {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return BatchID(fmt.Sprintf("batch_%d", s.seq))
}

// FindBatch looks up a batch by ID
func FindBatch(id BatchID) (*Batch, bool) {
	return batchStore.get(id)
}

// Batch tracks the state of a group of jobs
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

	thenFn    func(b *Batch)
	catchFn   func(b *Batch, err error)
	finallyFn func(b *Batch)
	catchOnce sync.Once

	dispatchEvent func(event interface{})

	mu sync.Mutex // protects finishedAt and callback execution
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

// Cancel cancels the batch, preventing remaining jobs from being processed.
func (b *Batch) Cancel() {
	if b.cancelled.CompareAndSwap(false, true) {
		dispatchBatchEvent(b.dispatchEvent, &BatchCancelled{
			BatchID:    string(b.id),
			FailedJobs: b.FailedJobs(),
		})
	}
}

// recordSuccess is called by the worker when a batch job completes successfully
func (b *Batch) recordSuccess() {
	b.pendingJobs.Add(-1)
	completed := b.completedJobs.Add(1)

	dispatchBatchEvent(b.dispatchEvent, &BatchJobCompleted{
		BatchID:       string(b.id),
		CompletedJobs: int(completed),
		TotalJobs:     b.totalJobs,
		Progress:      b.Progress(),
	})

	b.checkFinished()
}

// recordFailure is called by the worker when a batch job fails permanently
func (b *Batch) recordFailure(err error) {
	b.pendingJobs.Add(-1)
	failed := b.failedJobs.Add(1)

	dispatchBatchEvent(b.dispatchEvent, &BatchJobFailed{
		BatchID:    string(b.id),
		FailedJobs: int(failed),
		TotalJobs:  b.totalJobs,
		Error:      err.Error(),
	})

	// Fire catch callback once on first failure. Wrap in async.Go so a panic
	// inside user-supplied code is recovered and logged rather than crashing
	// the worker process.
	b.catchOnce.Do(func() {
		if b.catchFn != nil {
			catchFn := b.catchFn
			catchErr := err
			async.Go(func() { catchFn(b, catchErr) })
		}
	})

	// Auto-cancel if failures not allowed
	if !b.allowFailures {
		b.Cancel()
	}

	b.checkFinished()
}

// checkFinished checks if all jobs have been processed and fires callbacks
func (b *Batch) checkFinished() {
	if b.pendingJobs.Load() > 0 {
		return
	}

	if !b.finished.CompareAndSwap(false, true) {
		return // already finished
	}

	b.mu.Lock()
	b.finishedAt = time.Now()
	thenFn := b.thenFn
	finallyFn := b.finallyFn
	b.mu.Unlock()

	// Fire then callback if no failures. Wrap in async.Go so a panic inside
	// user-supplied code is recovered and logged rather than crashing the process.
	if !b.HasFailures() && thenFn != nil {
		fn := thenFn
		async.Go(func() { fn(b) })
	}

	// Always fire finally, also wrapped for panic safety.
	if finallyFn != nil {
		fn := finallyFn
		async.Go(func() { fn(b) })
	}

	dispatchBatchEvent(b.dispatchEvent, &BatchCompleted{
		BatchID:       string(b.id),
		TotalJobs:     b.totalJobs,
		CompletedJobs: b.CompletedJobs(),
		FailedJobs:    b.FailedJobs(),
		HasFailures:   b.HasFailures(),
	})
}

// dispatchBatchEvent dispatches a batch event (nil-safe like other dispatch helpers)
func dispatchBatchEvent(dispatch func(interface{}), event interface{}) {
	if dispatch == nil {
		return
	}
	dispatch(event)
}

// PendingBatch is a fluent builder for creating and dispatching a batch
type PendingBatch struct {
	jobs          []Job
	thenFn        func(b *Batch)
	catchFn       func(b *Batch, err error)
	finallyFn     func(b *Batch)
	allowFailures bool
	queue         string
	dispatchEvent func(event interface{})
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

// WithEventDispatcher sets the event dispatcher for the batch
func (pb *PendingBatch) WithEventDispatcher(fn func(event interface{})) *PendingBatch {
	pb.dispatchEvent = fn
	return pb
}

// Dispatch creates the batch, sets BatchID on Batchable jobs, and pushes all jobs to the driver.
func (pb *PendingBatch) Dispatch(driver Driver) (*Batch, error) {
	if len(pb.jobs) == 0 {
		return nil, fmt.Errorf("batch: cannot dispatch empty batch")
	}

	id := batchStore.nextID()

	batch := &Batch{
		id:            id,
		totalJobs:     len(pb.jobs),
		allowFailures: pb.allowFailures,
		queue:         pb.queue,
		thenFn:        pb.thenFn,
		catchFn:       pb.catchFn,
		finallyFn:     pb.finallyFn,
		dispatchEvent: pb.dispatchEvent,
	}
	batch.pendingJobs.Store(int32(len(pb.jobs)))

	batchStore.store(batch)

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
		if err := driver.Push(job, queueName); err != nil {
			// Adjust pendingJobs to reflect only the jobs that were actually pushed,
			// then cancel the batch so it can still reach Finished state.
			unpushed := int32(len(pb.jobs) - pushed)
			batch.pendingJobs.Add(-unpushed)
			batch.Cancel()
			return batch, fmt.Errorf("batch: failed to push job %d/%d: %w", pushed+1, len(pb.jobs), err)
		}
		pushed++
	}

	dispatchBatchEvent(pb.dispatchEvent, &BatchCreated{
		BatchID:   string(id),
		TotalJobs: len(pb.jobs),
		Queue:     pb.queue,
	})

	return batch, nil
}
