package queue

import (
	"container/heap"
	"container/list"
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// dedupeKeyRetention is the horizon past which a dedupe key recorded by
// PushIfNotExistsCtx is swept from m.dedupeKeys. Matches the redis
// sentinel TTL (queue/redis: 7*24*60*60s) and the database job_dedupe
// prune horizon so the three drivers converge on one at-most-once
// memory window. Keys are held this long (well past any callback's
// execution window) so a stale reaper re-push after the original entry
// was consumed cannot re-enqueue it; see popLocked.
const dedupeKeyRetention = 7 * 24 * time.Hour

// dedupeSweepInterval is how often processDelayedJobs prunes stale
// dedupe keys. Coarse relative to the 1s delayed-job tick: the set is
// small and entries live for dedupeKeyRetention, so an hourly sweep
// bounds growth without scanning the map every second.
const dedupeSweepInterval = time.Hour

// MemoryDriver implements Queue interface using in-memory storage
type MemoryDriver struct {
	// DriverCore supplies the lock-free event-dispatch slot (SetEventDispatcher
	// / DispatchEvent) shared by every built-in driver. Embedded so the
	// promoted SetEventDispatcher satisfies contract.EventDispatcherAware.
	DriverCore

	mu       sync.RWMutex
	queues   map[string]*list.List
	delayed  map[string]*delayedHeap
	failed   map[string][]*failedJob
	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
	// dedupeKeys tracks the dedupe identifiers of jobs that have been
	// pushed via PushIfNotExistsCtx, mapped to their insertion time.
	// Implements DedupeAwarePusher: a PushIfNotExistsCtx whose key is
	// already present returns nil without inserting. Entries are
	// INTENTIONALLY retained past Pop (see popLocked) to close the C-03
	// fb4 re-push hole; they are removed only by Clear (queue-scoped) and
	// by the background sweep in processDelayedJobs, which prunes entries
	// older than dedupeKeyRetention (7 days) ONLY once they no longer
	// belong to a live job (see sweepStaleDedupeKeys), matching the redis
	// sentinel TTL and the database job_dedupe prune horizon. Guarded by m.mu
	// (same as queues / delayed) so the lookup and the insert are atomic
	// against concurrent Push.
	dedupeKeys map[string]time.Time
	// reservations holds wrappers leased to a worker by PopCtxReserved
	// keyed by the ReservationToken.ID. The wrapper carries the
	// persisted attempts counter (Payload.Attempts) which the pop path
	// increments BEFORE handing the job to the worker. This is the
	// memory-driver counterpart to the DB driver's jobs.attempts
	// column: it is the authoritative source for MaxAttempts decisions
	// across multiple worker goroutines sharing one driver, where the
	// per-worker sync.Map cache resets on each Pop (jobKey is the job
	// pointer for non-Identifiable jobs, and the pointer changes when a
	// fresh pop returns a different wrapper struct).
	reservations map[int64]*memReservation
	// nextReservationID is a monotonic counter feeding ReservationToken.ID.
	// Starts at 1 so 0 stays reserved for the IsZero contract. Read and
	// written under m.mu.
	nextReservationID int64

	// nonIdentifiableWarned ensures the "this job kind cannot reliably
	// hit MaxAttempts across processes" advisory fires at most once per
	// distinct job type. Without the gate, a chatty queue would spam
	// the log every push.
	nonIdentifiableWarned sync.Map // keyed by job type string

	// logger is stored in an atomic.Value so the shutdown panic-recovery
	// path can read it without acquiring the main lock (the goroutine
	// that observes a panic may be fired under arbitrary locking
	// conditions).
	logger atomic.Value // holds memLoggerHolder{Logger}
}

// memReservation holds a wrapper leased to a worker via PopCtxReserved
// along with the queue name it came from. Holding the wrapper (not a
// fresh copy) means Attempts written on the wrapper inside Pop is the
// same value seen on Release / Ack / FailReserved.
type memReservation struct {
	wrapper *jobWrapper
	queue   string
}

// Compile-time assertion that MemoryDriver implements ReservationDriver.
// Same shape as DatabaseDriver: the worker prefers PopCtxReserved when
// available, which is the only path that surfaces persisted attempts on
// the token (see Worker.attemptNumber).
var (
	_ Driver            = (*MemoryDriver)(nil)
	_ TraceAwareDriver  = (*MemoryDriver)(nil)
	_ ReservationDriver = (*MemoryDriver)(nil)
	_ DedupeAwarePusher = (*MemoryDriver)(nil)
)

// memLoggerHolder wraps a Logger so atomic.Value stores a single concrete type.
type memLoggerHolder struct{ Logger }

// SetLogger installs a logger for operational events (shutdown-time
// panic recovery). Nil disables logging. Safe to call concurrently.
func (m *MemoryDriver) SetLogger(l Logger) {
	m.logger.Store(memLoggerHolder{Logger: l})
}

// log returns the installed logger, or nil when SetLogger has not been called.
func (m *MemoryDriver) log() Logger {
	v := m.logger.Load()
	if v == nil {
		return nil
	}
	return v.(memLoggerHolder).Logger
}

type delayedJob struct {
	wrapper *jobWrapper
	runAt   time.Time
	index   int // heap position, maintained by container/heap
}

// delayedHeap is a min-heap of *delayedJob ordered by runAt.
// Implements container/heap.Interface so ready jobs can be popped in O(log n).
type delayedHeap struct {
	items []*delayedJob
}

func (h *delayedHeap) Len() int { return len(h.items) }
func (h *delayedHeap) Less(i, j int) bool {
	return h.items[i].runAt.Before(h.items[j].runAt)
}
func (h *delayedHeap) Swap(i, j int) {
	h.items[i], h.items[j] = h.items[j], h.items[i]
	h.items[i].index = i
	h.items[j].index = j
}
func (h *delayedHeap) Push(x any) {
	j := x.(*delayedJob)
	j.index = len(h.items)
	h.items = append(h.items, j)
}
func (h *delayedHeap) Pop() any {
	n := len(h.items)
	j := h.items[n-1]
	h.items = h.items[:n-1]
	j.index = -1
	return j
}
func (h *delayedHeap) peek() *delayedJob {
	if len(h.items) == 0 {
		return nil
	}
	return h.items[0]
}

type failedJob struct {
	wrapper  *jobWrapper
	job      Job
	error    string
	failedAt time.Time
}

// NewMemoryDriver creates a new memory queue driver.
// Call Start() to begin the background delayed-job processor.
func NewMemoryDriver() *MemoryDriver {
	return &MemoryDriver{
		queues:       make(map[string]*list.List),
		delayed:      make(map[string]*delayedHeap),
		failed:       make(map[string][]*failedJob),
		dedupeKeys:   make(map[string]time.Time),
		reservations: make(map[int64]*memReservation),
		stopChan:     make(chan struct{}),
	}
}

// Start begins the background goroutine that moves delayed jobs to the
// main queue when their delay has elapsed. Must be called after construction.
// The goroutine runs via async.Go so any panic is caught and logged rather
// than tearing down the process.
func (m *MemoryDriver) Start() {
	m.wg.Add(1)
	async.Go(func() {
		defer m.wg.Done()
		m.processDelayedJobs()
	})
}

// warnIfNonIdentifiable emits a one-time warning per distinct job type
// when the job does not implement Identifiable. The worker's in-memory
// attempts cache (Worker.attempts sync.Map) is keyed by the job pointer
// for non-Identifiable jobs; a job that fails on host A and is
// re-popped on host B (or even re-popped in the same process from a
// fresh wrapper after Pop returned a different pointer) sees attempts
// reset to zero, so MaxAttempts can never trip and a poison job loops
// forever. The reservation path (PopCtxReserved -> token.Attempts)
// patches the in-process case via Payload.Attempts on the wrapper, but
// it cannot rejoin attempts across crashes or driver restarts without
// an Identifiable.JobID() to key the persisted counter on.
//
// The warning is advisory only: existing jobs keep working with the
// best-effort cache. To surface the gap loudly per kind we gate on the
// fully-qualified type name in a sync.Map; once warned, the type stays
// quiet for the lifetime of the driver.
func (m *MemoryDriver) warnIfNonIdentifiable(job Job) {
	if _, ok := job.(Identifiable); ok {
		return
	}
	typ := fmt.Sprintf("%T", job)
	if _, loaded := m.nonIdentifiableWarned.LoadOrStore(typ, struct{}{}); loaded {
		return
	}
	if logger := m.log(); logger != nil {
		logger.Warn("velocity/queue: job type does not implement Identifiable; MaxAttempts cannot be enforced reliably across process restarts. Implement queue.Identifiable.JobID() to fix.",
			"type", typ,
		)
	}
}

// PushCtx adds a job to the queue. Honours ctx cancellation before the
// lock is acquired so a graceful-shutdown cancel on the caller doesn't
// queue new work.
func (m *MemoryDriver) PushCtx(ctx context.Context, job Job, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.warnIfNonIdentifiable(job)
	name := resolveQueueName(job, queueName...)

	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return err
	}
	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)

	m.mu.Lock()
	if _, exists := m.queues[name]; !exists {
		m.queues[name] = list.New()
	}
	m.queues[name].PushBack(wrapper)
	jobType := wrapper.Payload.Type
	m.mu.Unlock()

	// Dispatch events outside the lock so listeners that call back into
	// the driver (e.g. Push/Size) do not deadlock.
	dispatchJobQueued(m.DispatchEvent, ctx, jobType, name, false, 0)
	return nil
}

// PushDelayedCtx adds a job with a delay. Delayed jobs live in a per-queue
// min-heap keyed by readyAt so the cleanup loop drains ready jobs in
// O(log n) rather than scanning the full list every tick.
func (m *MemoryDriver) PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.warnIfNonIdentifiable(job)
	name := resolveQueueName(job, queueName...)

	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return err
	}
	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)

	m.mu.Lock()
	h, exists := m.delayed[name]
	if !exists {
		h = &delayedHeap{}
		m.delayed[name] = h
	}
	heap.Push(h, &delayedJob{
		wrapper: wrapper,
		runAt:   time.Now().Add(delay),
	})
	jobType := wrapper.Payload.Type
	m.mu.Unlock()

	// Dispatch events outside the lock so listeners that call back into
	// the driver (e.g. Push/Size) do not deadlock.
	dispatchJobQueued(m.DispatchEvent, ctx, jobType, name, true, delay)
	return nil
}

// PopCtx retrieves and removes the next job. Returns (nil, ctx.Err()) if
// ctx is already cancelled so worker loops exit cleanly on shutdown.
func (m *MemoryDriver) PopCtx(ctx context.Context, queueName string) (Job, error) {
	job, _, err := m.PopCtxWithTrace(ctx, queueName)
	return job, err
}

// PopCtxWithTrace returns the popped job along with the producer-side trace
// context recovered from the in-memory wrapper. Implements TraceAwareDriver.
func (m *MemoryDriver) PopCtxWithTrace(ctx context.Context, queueName string) (Job, TraceContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, TraceContext{}, err
	}
	return m.popLocked(queueName)
}

func (m *MemoryDriver) popLocked(queueName string) (Job, TraceContext, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	q, exists := m.queues[queueName]
	if !exists || q.Len() == 0 {
		return nil, TraceContext{}, nil // No jobs available
	}

	element := q.Front()
	q.Remove(element)

	wrapper, ok := element.Value.(*jobWrapper)
	if !ok {
		return nil, TraceContext{}, fmt.Errorf("invalid wrapper type")
	}

	// The dedupe key is INTENTIONALLY NOT released here. Holding the
	// key past Pop is what makes the queue-layer at-most-once contract
	// robust against the failure mode that drove C-03 fb4: a
	// successful push whose MarkCallbackDispatched then failed, the
	// worker consumes and runs the callback to completion, and a
	// stale reaper tick later attempts a re-push. With the key still
	// present, PushIfNotExistsCtx no-ops on the retry. The set does not
	// leak: the background sweep in processDelayedJobs prunes keys older
	// than dedupeKeyRetention (7 days), which is well past any callback's
	// execution window and matches the redis/database sibling horizons.
	_ = wrapper.DedupeKey

	tc := TraceContext{}
	if wrapper.Payload != nil {
		tc.TraceID = wrapper.Payload.TraceID
		tc.SpanID = wrapper.Payload.SpanID
		tc.ParentID = wrapper.Payload.ParentID
	}

	// Same-process pop: wrapper.Job is non-nil and is returned directly via
	// the fast path inside getJobFromWrapper. Error path covers wrappers
	// rebuilt from bytes (defensive; the memory driver never produces those).
	job, err := getJobFromWrapper(wrapper)
	if err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to restore job from wrapper: %w", err)
	}
	return job, tc, nil
}

// PopCtxReserved leases the next available job for the worker. The
// wrapper's Payload.Attempts counter is incremented BEFORE the job is
// returned and the post-increment value is surfaced on the
// ReservationToken so the worker treats the persisted attempts as the
// authoritative source for MaxAttempts (see Worker.attemptNumber). A
// zero token paired with a nil job means "no job available".
//
// The wrapper is stashed in m.reservations under the new token ID
// until the worker calls AckCtx (delete), ReleaseCtx (re-enqueue with
// backoff), or FailReservedCtx (record in failed list). A driver
// Shutdown leaves reservations in place: this is an in-memory driver,
// so process exit is the end of the world and any in-flight jobs are
// lost regardless.
//
// Implements ReservationDriver.
func (m *MemoryDriver) PopCtxReserved(ctx context.Context, queueName string) (Job, ReservationToken, TraceContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, ReservationToken{}, TraceContext{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	q, exists := m.queues[queueName]
	if !exists || q.Len() == 0 {
		return nil, ReservationToken{}, TraceContext{}, nil
	}

	element := q.Front()
	q.Remove(element)

	wrapper, ok := element.Value.(*jobWrapper)
	if !ok {
		return nil, ReservationToken{}, TraceContext{}, fmt.Errorf("invalid wrapper type")
	}

	tc := TraceContext{}
	if wrapper.Payload != nil {
		tc.TraceID = wrapper.Payload.TraceID
		tc.SpanID = wrapper.Payload.SpanID
		tc.ParentID = wrapper.Payload.ParentID
		// Persist the incremented counter on the wrapper so a
		// subsequent Release/re-pop reads it back.
		wrapper.Payload.Attempts++
	}

	job, err := getJobFromWrapper(wrapper)
	if err != nil {
		return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to restore job from wrapper: %w", err)
	}

	m.nextReservationID++
	id := m.nextReservationID
	m.reservations[id] = &memReservation{wrapper: wrapper, queue: queueName}

	attempts := 0
	if wrapper.Payload != nil {
		attempts = wrapper.Payload.Attempts
	}
	return job, ReservationToken{ID: id, Attempts: attempts}, tc, nil
}

// AckCtx removes the reservation entry after the handler succeeded.
// Zero token is a no-op for symmetry with non-reserving call sites.
// Implements ReservationDriver.
func (m *MemoryDriver) AckCtx(ctx context.Context, token ReservationToken) error {
	if token.IsZero() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.reservations[token.ID]; !ok {
		// The reservation was already cleared (Clear, Shutdown, etc.).
		// Surface as lease-lost so the worker's success-side effects do
		// not double-fire on a redelivery.
		return ErrLeaseLost
	}
	delete(m.reservations, token.ID)
	return nil
}

// ReleaseCtx re-enqueues the leased wrapper on the delayed heap with
// the supplied backoff. The wrapper carries the incremented
// Payload.Attempts from PopCtxReserved, so the next pop reads the
// updated counter and the MaxAttempts budget shrinks as expected.
// Implements ReservationDriver.
func (m *MemoryDriver) ReleaseCtx(ctx context.Context, token ReservationToken, delay time.Duration) error {
	if token.IsZero() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay < 0 {
		delay = 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.reservations[token.ID]
	if !ok {
		return ErrLeaseLost
	}
	delete(m.reservations, token.ID)

	h, exists := m.delayed[r.queue]
	if !exists {
		h = &delayedHeap{}
		m.delayed[r.queue] = h
	}
	heap.Push(h, &delayedJob{
		wrapper: r.wrapper,
		runAt:   time.Now().Add(delay),
	})
	return nil
}

// FailReservedCtx records the job as failed and removes the reservation.
// Implements ReservationDriver.
func (m *MemoryDriver) FailReservedCtx(ctx context.Context, token ReservationToken, job Job, jobErr error, queueName string) error {
	if token.IsZero() {
		return m.Failed(job, jobErr, queueName)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	m.mu.Lock()
	r, ok := m.reservations[token.ID]
	if !ok {
		m.mu.Unlock()
		return ErrLeaseLost
	}
	delete(m.reservations, token.ID)

	if _, exists := m.failed[queueName]; !exists {
		m.failed[queueName] = make([]*failedJob, 0)
	}
	m.failed[queueName] = append(m.failed[queueName], &failedJob{
		wrapper:  r.wrapper,
		job:      job,
		error:    jobErr.Error(),
		failedAt: time.Now(),
	})
	m.mu.Unlock()

	// Job.Failed runs outside the lock so a handler that re-enters the
	// driver (Push/Size) does not self-deadlock.
	job.Failed(jobErr)
	return nil
}

// PushIfNotExistsCtx implements DedupeAwarePusher. The dedupe key is
// the deterministic identifier supplied by the caller (typically
// BatchCallbackJob.DedupeKey). Returns nil immediately when a live
// queue entry with the same key already exists; otherwise inserts the
// job and records the key. The lookup and the insert run under the
// same lock so concurrent pushes with the same key never produce
// duplicate entries.
//
// Empty dedupeKey falls through to PushCtx: the at-most-once contract
// requires a non-empty identifier, so we surface that as a programmer
// error via the regular push path rather than silently de-duping every
// keyless push.
func (m *MemoryDriver) PushIfNotExistsCtx(ctx context.Context, job Job, dedupeKey string, queueName ...string) error {
	if dedupeKey == "" {
		return m.PushCtx(ctx, job, queueName...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.warnIfNonIdentifiable(job)
	name := resolveQueueName(job, queueName...)

	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return err
	}
	wrapper.DedupeKey = dedupeKey
	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)

	m.mu.Lock()
	if _, exists := m.dedupeKeys[dedupeKey]; exists {
		// Already enqueued. Treat as success so the caller's reaper
		// stops retrying. We do NOT bump any retry counter or emit
		// events: the original push already did.
		m.mu.Unlock()
		return nil
	}
	if _, exists := m.queues[name]; !exists {
		m.queues[name] = list.New()
	}
	m.queues[name].PushBack(wrapper)
	m.dedupeKeys[dedupeKey] = time.Now()
	jobType := wrapper.Payload.Type
	m.mu.Unlock()

	dispatchJobQueued(m.DispatchEvent, ctx, jobType, name, false, 0)
	return nil
}

// Size returns the number of jobs in the queue
func (m *MemoryDriver) Size(queueName string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if q, exists := m.queues[queueName]; exists {
		return int64(q.Len()), nil
	}

	return 0, nil
}

// Clear removes all jobs from the queue
func (m *MemoryDriver) Clear(queueName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Release any dedupe keys associated with the cleared queue so a
	// subsequent PushIfNotExistsCtx for the same key inserts a fresh
	// row rather than silently no-op'ing against a stale entry.
	if q, ok := m.queues[queueName]; ok {
		for e := q.Front(); e != nil; e = e.Next() {
			if w, ok := e.Value.(*jobWrapper); ok && w.DedupeKey != "" {
				delete(m.dedupeKeys, w.DedupeKey)
			}
		}
	}
	if h, ok := m.delayed[queueName]; ok {
		for _, dj := range h.items {
			if dj.wrapper != nil && dj.wrapper.DedupeKey != "" {
				delete(m.dedupeKeys, dj.wrapper.DedupeKey)
			}
		}
	}
	// Reservations leased out of this queue still pin their dedupe key.
	// Release the key and drop the reservation so a later AckCtx surfaces
	// ErrLeaseLost (matching its "already cleared" contract) instead of
	// the key lingering and no-op'ing a same-key PushIfNotExistsCtx.
	for id, r := range m.reservations {
		if r.queue != queueName {
			continue
		}
		if r.wrapper != nil && r.wrapper.DedupeKey != "" {
			delete(m.dedupeKeys, r.wrapper.DedupeKey)
		}
		delete(m.reservations, id)
	}

	delete(m.queues, queueName)
	delete(m.delayed, queueName)

	return nil
}

// Failed moves a job to the failed queue
func (m *MemoryDriver) Failed(job Job, err error, queueName string) error {
	wrapper, serr := createJobWrapper(job, queueName)
	if serr != nil {
		return serr
	}

	m.mu.Lock()

	if _, exists := m.failed[queueName]; !exists {
		m.failed[queueName] = make([]*failedJob, 0)
	}

	m.failed[queueName] = append(m.failed[queueName], &failedJob{
		wrapper:  wrapper,
		job:      job,
		error:    err.Error(),
		failedAt: time.Now(),
	})
	m.mu.Unlock()

	// Job.Failed runs outside the lock so a handler that re-enters the
	// driver (Push/Size) does not self-deadlock. Mirrors FailReservedCtx.
	job.Failed(err)

	return nil
}

// GetFailed returns all failed jobs for a queue
func (m *MemoryDriver) GetFailed(queueName string) ([]*failedJob, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if failed, exists := m.failed[queueName]; exists {
		return failed, nil
	}

	return []*failedJob{}, nil
}

// Shutdown gracefully shuts down the driver, waiting for the background
// goroutine to finish. Honors the context deadline: if ctx expires before
// the goroutine exits, ctx.Err() is returned. Idempotent, safe to call
// multiple times.
func (m *MemoryDriver) Shutdown(ctx context.Context) error {
	// The batch repository is process-wide (see queue/batch_repository.go)
	// and outlives a single driver, so we no longer close it here.
	// Apps that explicitly install a custom default via
	// SetDefaultBatchRepository are responsible for Close()ing it from
	// their own shutdown path.
	m.stopOnce.Do(func() { close(m.stopChan) })

	done := make(chan struct{})
	// Recover so Shutdown always signals completion to the select below
	// even if wg.Wait panics (e.g. negative wait-group counter).
	// Not async.Go: must close(done) on panic so the outer select never
	// blocks shutdown waiting on a goroutine that already died.
	go func() { //safe-goroutine: close(done) on panic for shutdown, see comment above
		defer func() {
			if r := recover(); r != nil {
				if logger := m.log(); logger != nil {
					logger.Error("velocity/queue: memory driver shutdown panic recovered", "error", panicerr.FromRecovered(r))
				}
			}
			close(done)
		}()
		m.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// processDelayedJobs moves delayed jobs to main queue when ready.
// Runs until stopChan is closed by Shutdown. Caller decrements wg via Start().
func (m *MemoryDriver) processDelayedJobs() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	sweepTicker := time.NewTicker(dedupeSweepInterval)
	defer sweepTicker.Stop()

	for {
		select {
		case <-ticker.C:
			m.moveReadyJobs()
		case <-sweepTicker.C:
			m.sweepStaleDedupeKeys(time.Now())
		case <-m.stopChan:
			return
		}
	}
}

// sweepStaleDedupeKeys prunes dedupe keys whose insertion time is older
// than dedupeKeyRetention AND that no longer belong to a live job. Without
// this the set grows for the lifetime of the process: keys are
// intentionally held past Pop (see popLocked) and released elsewhere only
// by Clear. The `now` argument is a seam for deterministic tests. Runs
// under m.mu, the same lock guarding the map.
//
// A key is pruned only when it is a consumed tombstone: a job that long
// outlives the horizon while still queued, delayed, or reserved (e.g. a
// job repeatedly released with backoff) keeps its key so a second
// PushIfNotExistsCtx with the same key cannot enqueue a duplicate while
// the original is still in flight.
func (m *MemoryDriver) sweepStaleDedupeKeys(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	live := m.liveDedupeKeysLocked()
	for key, inserted := range m.dedupeKeys {
		if _, isLive := live[key]; isLive {
			continue
		}
		if now.Sub(inserted) >= dedupeKeyRetention {
			delete(m.dedupeKeys, key)
		}
	}
}

// liveDedupeKeysLocked returns the set of dedupe keys currently attached
// to a live job: queued, delayed, or reserved. Callers must hold m.mu.
// Failed jobs are terminal and deliberately excluded, so their key
// becomes a prunable tombstone once the horizon passes.
func (m *MemoryDriver) liveDedupeKeysLocked() map[string]struct{} {
	live := make(map[string]struct{})
	for _, q := range m.queues {
		for e := q.Front(); e != nil; e = e.Next() {
			if w, ok := e.Value.(*jobWrapper); ok && w.DedupeKey != "" {
				live[w.DedupeKey] = struct{}{}
			}
		}
	}
	for _, h := range m.delayed {
		for _, dj := range h.items {
			if dj.wrapper != nil && dj.wrapper.DedupeKey != "" {
				live[dj.wrapper.DedupeKey] = struct{}{}
			}
		}
	}
	for _, r := range m.reservations {
		if r.wrapper != nil && r.wrapper.DedupeKey != "" {
			live[r.wrapper.DedupeKey] = struct{}{}
		}
	}
	return live
}

// moveReadyJobs pops every ready delayed job from each per-queue heap and
// appends it to the main queue. Uses heap.Pop so each promotion is O(log n);
// the previous implementation rebuilt a slice on every tick which was O(n^2)
// in the worst case.
func (m *MemoryDriver) moveReadyJobs() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()

	for queueName, h := range m.delayed {
		for h.Len() > 0 {
			top := h.peek()
			if top.runAt.After(now) {
				break
			}
			job := heap.Pop(h).(*delayedJob)
			q, exists := m.queues[queueName]
			if !exists {
				q = list.New()
				m.queues[queueName] = q
			}
			q.PushBack(job.wrapper)
		}

		if h.Len() == 0 {
			delete(m.delayed, queueName)
		}
	}
}

// SerializeJob converts a job to a durable [Payload]. Thin compatibility
// wrapper around [MarshalJob]: callers that predate the C-01 fix can keep
// using SerializeJob unchanged. New code should call MarshalJob directly.
//
// SerializeJob never returns a "salvage" payload on marshal failure. The
// pre-C-01 implementation silently substituted `{"type":"%T"}` for jobs
// json.Marshal could not encode, which let the producer report success while
// guaranteeing the consumer could not reconstruct the job. The fix returns
// the marshal error so callers stop the push instead of dropping data.
func SerializeJob(job Job, queueName string) (*Payload, error) {
	return MarshalJob(job, queueName)
}
