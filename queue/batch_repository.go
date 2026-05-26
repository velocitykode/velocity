package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
)

// BatchRepository is the persistent store for batch metadata and counters.
//
// C-03: prior to this interface, batch state lived in a process-local
// `batchStore` map. Workers on a separate host that popped a Batchable job
// would call FindBatch, get (nil, false), and silently skip cancel checks
// and progress recording. Then/Catch/Finally callbacks fired only when the
// last job happened to land on the dispatcher host. Batch IDs collided
// across producers (sequential counter).
//
// The repository moves all mutating batch state behind a small interface so
// the queue package can be driven by either an in-process map (single-host
// deployments, tests) or a SQL table (multi-host worker fleets). Counter
// increments are atomic at the storage layer: the in-memory implementation
// uses `atomic.Int32`, the database implementation uses an UPDATE that
// decrements pending and increments completed/failed in one statement.
//
// All methods take a context so DB-backed implementations can honour
// shutdown deadlines. Implementations MUST be safe for concurrent use.
type BatchRepository interface {
	// Find loads a batch by ID. Returns (nil, nil) when not found (no error)
	// so callers can use a simple `if batch == nil` check; an error is
	// reserved for storage failures (DB unreachable, deserialisation, etc.).
	Find(ctx context.Context, id BatchID) (*Batch, error)

	// Save persists a new batch. Called once per batch at Dispatch time.
	// For DB implementations this is the INSERT; the in-memory version
	// stores into the map.
	Save(ctx context.Context, batch *Batch) error

	// IncrementSuccess records one successful job. Returns the refreshed
	// batch state and a `justFinished` flag that is true exactly once per
	// batch (the worker whose increment caused pendingJobs to hit zero).
	// Callers use justFinished to fire Then/Finally callbacks at most once
	// across the fleet (CAS on completed_at in the DB; CAS on `finished`
	// atomic.Bool in-memory).
	IncrementSuccess(ctx context.Context, id BatchID) (*Batch, bool, error)

	// IncrementFailure records one failed job. Same return semantics as
	// IncrementSuccess. The error is stored on the batch's last_error
	// column when available, so DB inspectors can see why a batch failed.
	IncrementFailure(ctx context.Context, id BatchID, jobErr error) (*Batch, bool, error)

	// Cancel marks the batch as cancelled. Idempotent: a second Cancel
	// is a no-op. Returns the refreshed batch state so callers can emit
	// the cancellation event with current counters.
	Cancel(ctx context.Context, id BatchID) (*Batch, error)

	// DecrementPending is invoked by the worker when it skips a job
	// (e.g. the batch was cancelled before the job could run). It
	// decrements pendingJobs without touching completed/failed, so the
	// batch can still reach Finished without the skipped job counting
	// as either success or failure. Returns justFinished like the
	// Increment* methods so the caller can fire Finally exactly once.
	DecrementPending(ctx context.Context, id BatchID) (*Batch, bool, error)

	// Delete removes a batch by ID. Used by tests; production callers
	// should rely on PruneStale instead.
	Delete(ctx context.Context, id BatchID) error

	// PruneStale removes batches whose completed_at is older than the
	// supplied duration. Returns the number of rows removed.
	PruneStale(ctx context.Context, olderThan time.Duration) (int, error)

	// MarkCallbackDispatched flips the row's dispatched flag for the
	// given callback kind. Invoked by the BatchCallbackJob dispatcher
	// after a successful PushCtx. The reaper uses the flag to skip
	// already-dispatched callbacks. Idempotent: a second call is a
	// no-op (returns nil) since the flag is monotonic.
	MarkCallbackDispatched(ctx context.Context, id BatchID, kind CallbackKind) error

	// FindUndispatchedCallbacks returns up to `limit` (id, kind, name,
	// errMsg) tuples for completed batches whose callback dispatch flag
	// is still false. Catch callbacks are returned independent of the
	// completed_at check because Catch can fire mid-flight on the first
	// failure; Then and Finally only return rows with non-null
	// completed_at. The reaper goroutine calls this every 15s and
	// retries enqueue for each row.
	FindUndispatchedCallbacks(ctx context.Context, limit int) ([]UndispatchedCallback, error)

	// Close releases any background resources (cleanup goroutines, DB
	// statements). Idempotent.
	Close() error
}

// UndispatchedCallback is the row tuple returned by
// FindUndispatchedCallbacks. The reaper enqueues one BatchCallbackJob
// per entry and then calls MarkCallbackDispatched on success. ErrMsg is
// populated only for Catch / Finally kinds where the originating
// failure text needs to thread through to the handler.
type UndispatchedCallback struct {
	BatchID BatchID
	Kind    CallbackKind
	Name    string
	ErrMsg  string
}

// callbackRegistry holds the in-memory Then/Catch/Finally closures and
// per-batch metadata that cannot survive a process boundary. Closures are
// keyed by BatchID and looked up by both the in-memory and database
// repositories when a batch transitions to its terminal state, so the
// dispatcher process always runs its own callbacks regardless of which
// repository implementation is wired.
//
// When a remote worker on a different process completes the last job,
// no entry exists in this registry (closures live only in the dispatcher
// process). In that case the repository emits the BatchCompleted event
// and the dispatcher process is expected to observe it through the
// event system, not via direct callback invocation. Callers wiring
// callbacks across processes should subscribe to BatchCompleted events.
type callbackRegistry struct {
	mu      sync.RWMutex
	entries map[BatchID]*callbackEntry
}

type callbackEntry struct {
	thenFn    func(b *Batch)
	catchFn   func(b *Batch, err error)
	finallyFn func(b *Batch)
	// catchOnce guards the Catch closure to fire exactly once per batch
	// from this process, even if multiple jobs fail concurrently.
	catchOnce sync.Once
	// finishedFired guards Then/Finally so they fire exactly once even
	// if the repository's justFinished signal races with a duplicate
	// terminal observation (defence-in-depth: the repo CAS already
	// guarantees justFinished is true exactly once).
	finishedFired atomic.Bool
	// dispatchEvent is the event dispatcher to use when emitting batch
	// lifecycle events. Stored here so the in-memory repo doesn't have
	// to round-trip through the Batch struct.
	dispatchEvent func(ctx context.Context, event interface{})
	// allowFailures controls whether Then fires when the batch has
	// failures. Cached here so callback firing decisions don't require
	// a repo round-trip.
	allowFailures bool
	// queue is the batch's queue name, kept for emitting events.
	queue string
	// totalJobs is the original job count, used for the BatchCompleted
	// event payload.
	totalJobs int
}

var globalCallbacks = &callbackRegistry{entries: make(map[BatchID]*callbackEntry)}

func (r *callbackRegistry) register(id BatchID, e *callbackEntry) {
	r.mu.Lock()
	r.entries[id] = e
	r.mu.Unlock()
}

func (r *callbackRegistry) get(id BatchID) *callbackEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[id]
}

func (r *callbackRegistry) remove(id BatchID) {
	r.mu.Lock()
	delete(r.entries, id)
	r.mu.Unlock()
}

// fireCatch invokes the Catch callback exactly once per batch from this
// process. Wrapped in async.Go so a panic inside user-supplied code is
// recovered and logged rather than crashing the worker.
func (e *callbackEntry) fireCatch(b *Batch, err error) {
	if e == nil {
		return
	}
	e.catchOnce.Do(func() {
		if e.catchFn != nil {
			fn := e.catchFn
			capturedErr := err
			async.Go(func() { fn(b, capturedErr) })
		}
	})
}

// fireFinished invokes Then (if no failures) and Finally exactly once.
// Caller is responsible for ensuring this is only called when the
// repository's CAS confirmed the batch transitioned to its terminal
// state. finishedFired is a defence-in-depth guard, not the primary
// synchronisation.
func (e *callbackEntry) fireFinished(b *Batch) {
	if e == nil {
		return
	}
	if !e.finishedFired.CompareAndSwap(false, true) {
		return
	}
	if !b.HasFailures() && e.thenFn != nil {
		fn := e.thenFn
		async.Go(func() { fn(b) })
	}
	if e.finallyFn != nil {
		fn := e.finallyFn
		async.Go(func() { fn(b) })
	}
}

// ----- in-memory repository --------------------------------------------------

// inMemoryBatchRepository is the default repository: a mutex-protected map
// of batch IDs to *Batch. This is the historical behaviour, retained as
// the default because (a) most users run a single worker process and
// (b) tests should not require a SQL connection.
type inMemoryBatchRepository struct {
	mu       sync.RWMutex
	batches  map[BatchID]*Batch
	stop     chan struct{}
	stopOnce sync.Once
}

// NewInMemoryBatchRepository constructs an in-memory repository and starts
// the periodic cleanup goroutine. Close() must be called to release it.
func NewInMemoryBatchRepository() BatchRepository {
	r := &inMemoryBatchRepository{
		batches: make(map[BatchID]*Batch),
		stop:    make(chan struct{}),
	}
	async.Go(func() { r.periodicCleanup() })
	return r
}

func (r *inMemoryBatchRepository) periodicCleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_, _ = r.PruneStale(context.Background(), 1*time.Hour)
		case <-r.stop:
			return
		}
	}
}

func (r *inMemoryBatchRepository) Find(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	b := r.batches[id]
	r.mu.RUnlock()
	return b, nil
}

func (r *inMemoryBatchRepository) Save(ctx context.Context, batch *Batch) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	r.batches[batch.id] = batch
	r.mu.Unlock()
	return nil
}

func (r *inMemoryBatchRepository) IncrementSuccess(ctx context.Context, id BatchID) (*Batch, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}
	b, _ := r.Find(ctx, id)
	if b == nil {
		return nil, false, nil
	}
	// Conditional increment: only bump completed_jobs when we actually
	// consumed a pending slot. Under duplicate delivery (worker crashed
	// after marking the job done but before deleting the queue row, then
	// a sibling worker re-popped it) the second IncrementSuccess would
	// otherwise push completed_jobs above total_jobs and lie about
	// progress. The atomic.Int32 CAS loop reserves exactly one slot.
	if !decrementPendingIfPositive(&b.pendingJobs) {
		return b, false, nil
	}
	b.completedJobs.Add(1)
	justFinished := false
	if b.pendingJobs.Load() <= 0 && b.finished.CompareAndSwap(false, true) {
		b.mu.Lock()
		b.finishedAt = time.Now()
		b.mu.Unlock()
		justFinished = true
	}
	return b, justFinished, nil
}

func (r *inMemoryBatchRepository) IncrementFailure(ctx context.Context, id BatchID, jobErr error) (*Batch, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}
	b, _ := r.Find(ctx, id)
	if b == nil {
		return nil, false, nil
	}
	// Conditional increment: same rationale as IncrementSuccess. A
	// duplicate failure (e.g. the same job rerun by another worker on
	// retry exhaustion) must not push failed_jobs past total_jobs.
	if !decrementPendingIfPositive(&b.pendingJobs) {
		return b, false, nil
	}
	b.failedJobs.Add(1)
	if jobErr != nil {
		b.mu.Lock()
		b.lastError = jobErr.Error()
		b.mu.Unlock()
	}
	justFinished := false
	if b.pendingJobs.Load() <= 0 && b.finished.CompareAndSwap(false, true) {
		b.mu.Lock()
		b.finishedAt = time.Now()
		b.mu.Unlock()
		justFinished = true
	}
	return b, justFinished, nil
}

func (r *inMemoryBatchRepository) Cancel(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	b, _ := r.Find(ctx, id)
	if b == nil {
		return nil, nil
	}
	b.cancelled.CompareAndSwap(false, true)
	return b, nil
}

func (r *inMemoryBatchRepository) DecrementPending(ctx context.Context, id BatchID) (*Batch, bool, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}
	b, _ := r.Find(ctx, id)
	if b == nil {
		return nil, false, nil
	}
	// Same clamp as IncrementSuccess/Failure: a no-op when pending is
	// already zero so duplicate skip notifications cannot drive the
	// counter negative.
	if !decrementPendingIfPositive(&b.pendingJobs) {
		return b, false, nil
	}
	justFinished := false
	if b.pendingJobs.Load() <= 0 && b.finished.CompareAndSwap(false, true) {
		b.mu.Lock()
		b.finishedAt = time.Now()
		b.mu.Unlock()
		justFinished = true
	}
	return b, justFinished, nil
}

func (r *inMemoryBatchRepository) Delete(ctx context.Context, id BatchID) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.batches, id)
	r.mu.Unlock()
	return nil
}

func (r *inMemoryBatchRepository) PruneStale(ctx context.Context, olderThan time.Duration) (int, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	r.mu.Lock()
	defer r.mu.Unlock()
	removed := 0
	for id, b := range r.batches {
		if b.finished.Load() && b.finishedAt.Before(cutoff) {
			delete(r.batches, id)
			globalCallbacks.remove(id)
			removed++
		}
	}
	return removed, nil
}

// MarkCallbackDispatched flips the in-memory dispatched flag for the
// given callback kind. The in-memory repo has no persistent storage so
// the flag lives on the Batch atomic.Bool fields; the reaper running
// on the DB repo is the one that actually retries on failures, but
// honouring the interface here lets the test suite exercise both code
// paths uniformly.
func (r *inMemoryBatchRepository) MarkCallbackDispatched(ctx context.Context, id BatchID, kind CallbackKind) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	b, _ := r.Find(ctx, id)
	if b == nil {
		return nil
	}
	switch kind {
	case CallbackThen:
		b.thenDispatched.Store(true)
	case CallbackCatch:
		b.catchDispatched.Store(true)
	case CallbackFinally:
		b.finallyDispatched.Store(true)
	}
	return nil
}

// FindUndispatchedCallbacks scans the in-memory map for completed
// batches whose Then/Finally dispatched flag is false (Catch is
// independent of completion). Returns at most `limit` rows; the
// reaper iterates these and PushCtx's a BatchCallbackJob for each.
func (r *inMemoryBatchRepository) FindUndispatchedCallbacks(ctx context.Context, limit int) ([]UndispatchedCallback, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []UndispatchedCallback
	for _, b := range r.batches {
		if len(out) >= limit {
			break
		}
		// Catch fires on first failure - independent of finished state.
		if b.catchName != "" && !b.catchDispatched.Load() && b.failedJobs.Load() > 0 {
			out = append(out, UndispatchedCallback{
				BatchID: b.id,
				Kind:    CallbackCatch,
				Name:    b.catchName,
				ErrMsg:  b.lastError,
			})
			if len(out) >= limit {
				break
			}
		}
		if !b.finished.Load() {
			continue
		}
		if b.thenName != "" && !b.thenDispatched.Load() && !(b.failedJobs.Load() > 0) {
			out = append(out, UndispatchedCallback{
				BatchID: b.id,
				Kind:    CallbackThen,
				Name:    b.thenName,
			})
			if len(out) >= limit {
				break
			}
		}
		if b.finallyName != "" && !b.finallyDispatched.Load() {
			out = append(out, UndispatchedCallback{
				BatchID: b.id,
				Kind:    CallbackFinally,
				Name:    b.finallyName,
				ErrMsg:  b.lastError,
			})
		}
	}
	return out, nil
}

func (r *inMemoryBatchRepository) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	return nil
}

// reset clears the repository state. Used by tests via resetBatchStoreForTest.
func (r *inMemoryBatchRepository) reset() {
	r.mu.Lock()
	r.batches = make(map[BatchID]*Batch)
	r.mu.Unlock()
}

// ctxErr returns ctx.Err() iff ctx is non-nil. A nil ctx is permitted as a
// shorthand for context.Background() since several call sites in the queue
// package use it that way.
func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// decrementPendingIfPositive atomically decrements an atomic.Int32 only
// when its current value is > 0. Returns true when the decrement was
// applied, false when the counter was already zero (or negative, which
// should be impossible but is treated as the same no-op case).
//
// The CAS loop is needed because plain Add(-1) followed by a Load can
// race with sibling workers and silently push the counter below zero.
// Used by the in-memory batch repository to make duplicate
// IncrementSuccess / IncrementFailure / DecrementPending calls safe:
// the success / failure counter is only bumped when this call actually
// consumed a pending slot, so the invariant
//
//	completed_jobs + failed_jobs <= total_jobs
//
// holds even when the queue layer delivers the same job twice.
func decrementPendingIfPositive(p *atomic.Int32) bool {
	for {
		cur := p.Load()
		if cur <= 0 {
			return false
		}
		if p.CompareAndSwap(cur, cur-1) {
			return true
		}
	}
}

// ----- default repository accessor ------------------------------------------

// defaultBatchRepo is the process-wide repository used by FindBatch and by
// Batches dispatched without an explicit repository. Stored in an atomic
// pointer so SetDefaultBatchRepository is lock-free and safe to call from
// app wiring goroutines.
var defaultBatchRepo atomic.Pointer[batchRepoHolder]

// batchRepoHolder carries the repository pointer plus two flags that
// classify how it was installed:
//
//   - userSet=true means the application called SetDefaultBatchRepository
//     explicitly. This holder survives app Shutdown so embedded apps and
//     test harnesses that pre-wire a repo do not get clobbered.
//
//   - autoInstalled=true means the framework's initQueue auto-wired the
//     repo because Config.Queue.Driver=database. App.Shutdown calls
//     ResetAutoInstalledBatchRepository to drop these back to the
//     package-init in-memory default so a subsequent velocity.New on the
//     same process can install its own DB-backed repo against the new
//     *sql.DB. Without the reset, a re-entrant New would either keep
//     using the previous app's (now-closed) DB handle or skip auto-
//     install entirely because userSet was set on the previous boot.
//
// The init() holder has both flags false so the boot-time auto-install
// can detect "still on the default" and upgrade.
type batchRepoHolder struct {
	BatchRepository
	userSet       bool
	autoInstalled bool
}

func init() {
	defaultBatchRepo.Store(&batchRepoHolder{BatchRepository: NewInMemoryBatchRepository()})
}

// DefaultBatchRepository returns the process-wide batch repository. It is
// never nil: at package init time the in-memory implementation is
// installed; callers may replace it via SetDefaultBatchRepository.
func DefaultBatchRepository() BatchRepository {
	h := defaultBatchRepo.Load()
	return h.BatchRepository
}

// SetDefaultBatchRepository installs a new process-wide repository. The
// previous repository is Close()d to release its resources (cleanup
// goroutine, prepared statements). Typical wiring: apps using the database
// queue driver call this from main() with a DatabaseBatchRepository so
// FindBatch and worker counters route through the SQL table.
//
// Panics if repo is nil; accepting nil would silently break batch
// dispatching since every worker path goes through this accessor.
func SetDefaultBatchRepository(repo BatchRepository) {
	if repo == nil {
		panic("velocity/queue: SetDefaultBatchRepository called with nil repository")
	}
	prev := defaultBatchRepo.Swap(&batchRepoHolder{BatchRepository: repo, userSet: true})
	if prev != nil && prev.BatchRepository != nil {
		_ = prev.BatchRepository.Close()
	}
}

// EnsureDefaultBatchRepository installs repo only when the process is
// still on the package-init in-memory default. Apps that have already
// wired a custom repo via SetDefaultBatchRepository are left untouched,
// which is what makes auto-install at app boot safe: the framework's
// initQueue calls this when Config.Queue.Driver=database, but a user
// who preemptively installed (e.g. a custom Redis-backed repo) is not
// overwritten.
//
// The installed holder is marked autoInstalled=true so App.Shutdown can
// drop it back to the in-memory default via
// ResetAutoInstalledBatchRepository. A subsequent velocity.New in the
// same process therefore observes the package-init state and installs
// its own DB-backed repo against the new *sql.DB, instead of inheriting
// the previous app's (now-closed) repository pointer.
//
// Returns true when repo was installed, false when the default was
// already user-set (in which case repo is closed to avoid leaking the
// cleanup goroutine / prepared statements it would have spun up).
//
// Panics if repo is nil for the same reason SetDefaultBatchRepository
// does: silently installing nil would break every worker code path.
func EnsureDefaultBatchRepository(repo BatchRepository) bool {
	if repo == nil {
		panic("velocity/queue: EnsureDefaultBatchRepository called with nil repository")
	}
	cur := defaultBatchRepo.Load()
	if cur != nil && cur.userSet {
		// Caller already wired something explicitly; don't clobber it.
		// Close repo so its resources are not leaked.
		_ = repo.Close()
		return false
	}
	newHolder := &batchRepoHolder{BatchRepository: repo, userSet: true, autoInstalled: true}
	if !defaultBatchRepo.CompareAndSwap(cur, newHolder) {
		// Lost a race with another EnsureDefault / SetDefault call.
		// Close repo and let the winner stand.
		_ = repo.Close()
		return false
	}
	if cur != nil && cur.BatchRepository != nil {
		_ = cur.BatchRepository.Close()
	}
	return true
}

// ResetAutoInstalledBatchRepository swaps the default back to a fresh
// in-memory repo IFF the current holder was installed via
// EnsureDefaultBatchRepository (autoInstalled=true). Holders installed
// via SetDefaultBatchRepository (userSet=true && autoInstalled=false)
// are explicit caller choices and are left alone.
//
// This is the App.Shutdown hook for C-03-fb2 HIGH 2: without it, a
// second velocity.New in the same process would keep using the first
// app's *sql.DB handle for batch state because EnsureDefault would
// short-circuit on userSet=true.
//
// Returns true when a reset occurred, false when the current holder
// belongs to an explicit caller or the package-init default is still
// in place.
func ResetAutoInstalledBatchRepository() bool {
	cur := defaultBatchRepo.Load()
	if cur == nil || !cur.autoInstalled {
		return false
	}
	fresh := &batchRepoHolder{BatchRepository: NewInMemoryBatchRepository()}
	if !defaultBatchRepo.CompareAndSwap(cur, fresh) {
		// Concurrent SetDefault won the swap; their choice stands.
		_ = fresh.BatchRepository.Close()
		return false
	}
	if cur.BatchRepository != nil {
		_ = cur.BatchRepository.Close()
	}
	return true
}
