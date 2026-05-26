package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/velocitykode/velocity/async"
)

// batchCallbackUUIDNamespace is the v5 UUID namespace under which all
// BatchCallbackJob dedupe keys are derived. The namespace is fixed at
// package init from a stable seed string so a deterministic UUID can be
// computed on any host from (batchID, kind) alone, with no shared state.
// UUID v5 (SHA1) is preferred over v3 (MD5) because v5 is the modern
// recommendation in RFC 4122 errata and because google/uuid exposes it
// as `NewSHA1`.
var batchCallbackUUIDNamespace = uuid.NewSHA1(uuid.NameSpaceURL,
	[]byte("https://velocity.dev/queue/batch-callback"))

// CallbackKind identifies which of Then / Catch / Finally a queued
// callback corresponds to. Persisted as a string so the queue payload
// is human-readable when an operator inspects a stuck row.
type CallbackKind string

const (
	// CallbackThen runs once when every job in the batch completes
	// without failure.
	CallbackThen CallbackKind = "then"
	// CallbackCatch runs once on the first job failure observed by
	// the fleet.
	CallbackCatch CallbackKind = "catch"
	// CallbackFinally runs once when the batch reaches its terminal
	// state, regardless of failure mode. Always fires last.
	CallbackFinally CallbackKind = "finally"
)

// BatchCallbackFunc is the success/finally signature: it receives the
// reconstructed *Batch (counters as observed at completion).
type BatchCallbackFunc func(ctx context.Context, b *Batch) error

// BatchFailureCallbackFunc is the catch signature: it receives the
// reconstructed *Batch and a string description of the first failure
// (full error chain text is not portable across processes, so we
// preserve the message only).
type BatchFailureCallbackFunc func(ctx context.Context, b *Batch, errMsg string) error

// batchCallbackRegistry maps callback names to handler functions.
// Cross-process callback delivery (C-03 fb2) hinges on this registry:
// the dispatcher persists the callback NAME on the batch row, then
// enqueues a BatchCallbackJob when the terminal CAS fires. Any worker
// that pops the job looks up the name here and runs the function. The
// registry must therefore be populated on every worker host (typically
// from main() via RegisterBatchCallback / RegisterBatchFailureCallback)
// for the cross-process path to work.
type batchCallbackRegistry struct {
	mu               sync.RWMutex
	successCallbacks map[string]BatchCallbackFunc
	failureCallbacks map[string]BatchFailureCallbackFunc
}

var batchCallbacks = &batchCallbackRegistry{
	successCallbacks: make(map[string]BatchCallbackFunc),
	failureCallbacks: make(map[string]BatchFailureCallbackFunc),
}

// RegisterBatchCallback registers a named handler invoked by Then or
// Finally callbacks across the worker fleet. Names are case-sensitive
// and must be stable across deploys: a rename leaves in-flight batches
// with a dangling reference that fails-on-dispatch (the handler lookup
// errors and the job retries per worker policy).
//
// Idempotent: a second Register with the same name overwrites the
// previous handler. This matches the queue.RegisterJob pattern so
// hot-reload scenarios behave the same way.
//
// Panics if name is empty: an empty name would collide with the
// "no callback persisted" sentinel and silently drop completion events.
func RegisterBatchCallback(name string, fn BatchCallbackFunc) {
	if name == "" {
		panic("velocity/queue: RegisterBatchCallback name must not be empty")
	}
	batchCallbacks.mu.Lock()
	batchCallbacks.successCallbacks[name] = fn
	batchCallbacks.mu.Unlock()
}

// RegisterBatchFailureCallback registers a named handler for the Catch
// callback. Same semantics as RegisterBatchCallback but the handler
// receives the failure message.
func RegisterBatchFailureCallback(name string, fn BatchFailureCallbackFunc) {
	if name == "" {
		panic("velocity/queue: RegisterBatchFailureCallback name must not be empty")
	}
	batchCallbacks.mu.Lock()
	batchCallbacks.failureCallbacks[name] = fn
	batchCallbacks.mu.Unlock()
}

// lookupBatchCallback returns the success/finally handler by name. The
// boolean is false when no handler is registered (the worker logs and
// retries; callers must register every callback name they dispatch).
func lookupBatchCallback(name string) (BatchCallbackFunc, bool) {
	batchCallbacks.mu.RLock()
	defer batchCallbacks.mu.RUnlock()
	fn, ok := batchCallbacks.successCallbacks[name]
	return fn, ok
}

// lookupBatchFailureCallback is the catch-kind sibling of
// lookupBatchCallback.
func lookupBatchFailureCallback(name string) (BatchFailureCallbackFunc, bool) {
	batchCallbacks.mu.RLock()
	defer batchCallbacks.mu.RUnlock()
	fn, ok := batchCallbacks.failureCallbacks[name]
	return fn, ok
}

// ResetBatchCallbacksForTest clears the registry. Test-only.
func ResetBatchCallbacksForTest() {
	batchCallbacks.mu.Lock()
	batchCallbacks.successCallbacks = make(map[string]BatchCallbackFunc)
	batchCallbacks.failureCallbacks = make(map[string]BatchFailureCallbackFunc)
	batchCallbacks.mu.Unlock()
}

// BatchCallbackJob is the serialised form of a batch callback. It is
// enqueued by the batch terminal CAS so any worker (on any host) can
// execute the named handler. The job carries enough context to
// reconstruct the *Batch via FindBatch and to invoke the correct
// handler.
//
// Encoded payload format (registered with queue.RegisterJob at package
// init):
//
//	{"batch_id": "batch_<uuid>", "name": "send-summary", "kind": "then", "error": ""}
//
// The handler's identity is the (name, kind) pair: the same name can be
// used for both success and failure handlers without ambiguity because
// the worker looks up against separate maps.
type BatchCallbackJob struct {
	BatchID  BatchID      `json:"batch_id"`
	Name     string       `json:"name"`
	Kind     CallbackKind `json:"kind"`
	ErrorMsg string       `json:"error,omitempty"`
}

// JobID implements queue.Identifiable so the worker's attempt-tracking
// keyed by job ID survives serialisation. The ID is the deterministic
// dedupe key, which is itself a UUID v5 derived from (batchID, kind).
// Using the dedupe key as the JobID makes the worker's attempt-counter,
// the queue's dedupe lookup, and the reaper's idempotence all share one
// identifier.
func (j *BatchCallbackJob) JobID() string {
	return j.DedupeKey()
}

// DedupeKey returns the queue-layer deduplication key for this
// callback. Two BatchCallbackJob values with the same (BatchID, Kind)
// produce byte-identical keys, on any host, in any process. Drivers
// that implement DedupeAwarePusher reject a PushIfNotExistsCtx whose
// key already maps to a live queue row, so the reaper's retry path is
// idempotent at the queue layer regardless of whether
// MarkCallbackDispatched ever ran.
//
// The Name field is intentionally NOT part of the key: a batch only
// has one callback per kind (Then / Catch / Finally), so the
// (BatchID, Kind) pair is sufficient and stable across renames. If the
// dispatcher persisted a different Name and the reaper re-derived the
// key with the new Name, we'd lose dedupe.
func (j *BatchCallbackJob) DedupeKey() string {
	return uuid.NewSHA1(batchCallbackUUIDNamespace,
		[]byte(string(j.BatchID)+":"+string(j.Kind))).String()
}

// HandleCtx executes the callback. It looks up the named handler in
// the registry and invokes it with a freshly-loaded *Batch.
//
// Exactly-once delivery is guaranteed by the queue-layer dedupe in
// PushIfNotExistsCtx (see queue/database.go, queue/memory.go,
// queue/redis.go). The dedupe key for a BatchCallbackJob is the
// deterministic uuid5(batchID, kind); the dedupe row persists past Pop
// (and is pruned on a long horizon by PruneStaleDedupeKeys / TTL) so
// any stale reaper retry after the original job was consumed is a
// no-op at the storage layer. The handler therefore does not need a
// secondary "have I already run?" check; one BatchCallbackJob per
// (batchID, kind) is the invariant the dedupe key enforces.
//
// Errors:
//   - ErrJobNotFound style: handler name not in registry. We return an
//     error so the worker retries; an operator rolling out a release
//     that adds a new callback handler can tolerate the race where a
//     callback job lands on a worker that hasn't deployed the new
//     handler yet.
//   - The handler itself returned an error. Surfaced as a job failure
//     so the worker's retry / failed_jobs plumbing handles it.
//
// Handle invokes HandleCtx with context.Background for legacy callers.
func (j *BatchCallbackJob) HandleCtx(ctx context.Context) error {
	b, err := DefaultBatchRepository().Find(ctx, j.BatchID)
	if err != nil {
		return fmt.Errorf("velocity/queue: batch callback %s/%s: load batch %s: %w",
			j.Kind, j.Name, j.BatchID, err)
	}
	if b == nil {
		// Batch was pruned between dispatch and execution. Nothing
		// the callback can do; treat as a successful no-op so the
		// worker does not retry into the failed_jobs table.
		return nil
	}

	switch j.Kind {
	case CallbackThen, CallbackFinally:
		fn, ok := lookupBatchCallback(j.Name)
		if !ok {
			return fmt.Errorf("velocity/queue: batch callback %s/%s: handler not registered on this worker",
				j.Kind, j.Name)
		}
		return fn(ctx, b)
	case CallbackCatch:
		fn, ok := lookupBatchFailureCallback(j.Name)
		if !ok {
			return fmt.Errorf("velocity/queue: batch callback %s/%s: handler not registered on this worker",
				j.Kind, j.Name)
		}
		return fn(ctx, b, j.ErrorMsg)
	default:
		return fmt.Errorf("velocity/queue: batch callback: unknown kind %q", j.Kind)
	}
}

// Handle satisfies queue.Job for drivers that do not invoke HandleCtxer.
func (j *BatchCallbackJob) Handle() error { return j.HandleCtx(context.Background()) }

// Failed satisfies queue.Job: callback failure is logged via the worker's
// failed_jobs path. No special handling here.
func (j *BatchCallbackJob) Failed(error) {}

// MaxAttempts caps callback retries so a permanently-broken handler does
// not flood the queue. Three is the default but the value is small and
// hardcoded here because callback semantics differ from app jobs:
// retrying Then forever serves no one. Apps that need different limits
// can wrap the handler.
func (j *BatchCallbackJob) MaxAttempts() int { return 3 }

func init() {
	// Register BatchCallbackJob so the framework's serializer can
	// reconstruct it on the worker side. Mirrors the pattern any
	// queueable job must use.
	RegisterJob(func(data []byte) (*BatchCallbackJob, error) {
		j := &BatchCallbackJob{}
		return j, json.Unmarshal(data, j)
	})
}

// pushBatchCallback enqueues a BatchCallbackJob via PushIfNotExistsCtx
// when the driver implements DedupeAwarePusher, falling back to PushCtx
// when it does not. The dedupe key is the deterministic
// uuid5(batchID, kind) returned by BatchCallbackJob.DedupeKey: two
// independent dispatch attempts for the same (batchID, kind) collide
// at the driver layer, so a reaper retry after a failed
// MarkCallbackDispatched cannot enqueue a second copy.
//
// Returns the push error so the caller (the dispatcher async path or
// the reaper) can decide whether to set the *_dispatched flag.
func pushBatchCallback(ctx context.Context, driver Driver, queueName string, job *BatchCallbackJob) error {
	if d, ok := driver.(DedupeAwarePusher); ok {
		return d.PushIfNotExistsCtx(ctx, job, job.DedupeKey(), queueName)
	}
	return driver.PushCtx(ctx, job, queueName)
}

// dispatchBatchCallbackJob enqueues a BatchCallbackJob via
// pushBatchCallback (which uses the queue's at-most-once primitive when
// available) and, on a successful push, marks the batch row's
// `<kind>_dispatched` column to true.
//
// The C-03 fb4 fix: the push path uses PushIfNotExistsCtx with a
// deterministic dedupe key. If MarkCallbackDispatched then fails (or
// the process crashes between push and mark), the reaper's retry
// re-issues PushIfNotExistsCtx for the same key, which the driver
// recognises as a duplicate and no-ops. No second queue row is
// inserted; the original job runs exactly once.
//
// Failure handling for the push itself: PushIfNotExistsCtx errors are
// NOT swallowed. The function leaves *_dispatched=false so the reaper
// retries against the persisted state on its next tick.
//
// The driver.Push runs in async.Go so the worker invoking the
// terminal CAS does not block on driver I/O. The reaper is the
// load-bearing durability path: even if the async goroutine here is
// preempted indefinitely or the process crashes, the reaper retries
// from the persisted state.
//
// Returns immediately when no callback queue driver is wired (e.g.
// memory-only tests). The reaper handles that case too: it skips
// dispatch when the driver is nil so a later SetBatchCallbackQueue
// wiring picks up the backlog.
func dispatchBatchCallbackJob(ctx context.Context, name string, kind CallbackKind, batchID BatchID, errMsg string) {
	driver := callbackQueueDriver()
	if driver == nil || name == "" {
		return
	}
	job := &BatchCallbackJob{
		BatchID:  batchID,
		Name:     name,
		Kind:     kind,
		ErrorMsg: errMsg,
	}
	queueName := callbackQueueName()
	async.Go(func() {
		// Short timeout: callback enqueue should never hold up worker
		// shutdown. On timeout / driver error the reaper retries
		// against the persisted state on its next tick.
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := pushBatchCallback(pushCtx, driver, queueName, job); err != nil {
			// Enqueue failed: leave dispatched=false so the reaper
			// retries. We intentionally do NOT log here because the
			// reaper runs every 15s and a flood of failed PushCtx
			// from a sustained Redis outage would otherwise spam the
			// log. Operators see the symptom (batches stuck with
			// dispatched=false in job_batches) via dashboards.
			return
		}
		// Successful enqueue: mark dispatched=true so the reaper
		// stops re-issuing this job. A fresh background context with
		// a short timeout is used because the caller's ctx may have
		// already been cancelled by worker shutdown.
		//
		// If THIS MarkCallbackDispatched fails, the queue-layer
		// dedupe (the DedupeAwarePusher path above) still prevents
		// a second enqueue when the reaper retries: the dedupe key
		// is in job_dedupe (or the memory map / Redis SETNX) so
		// PushIfNotExistsCtx no-ops cleanly.
		markCtx, markCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer markCancel()
		_ = DefaultBatchRepository().MarkCallbackDispatched(markCtx, batchID, kind)
	})
	// ctx is honoured indirectly via async.Go's recovery, but we still
	// accept it for symmetry with sibling dispatch helpers.
	_ = ctx
}

// callbackQueueDriver / callbackQueueName let the framework wire the
// process-wide queue driver and queue name without queue/ importing
// any other framework package. The pointers are atomically swapped by
// SetBatchCallbackQueue (called from initQueue once the driver is up).
var (
	callbackQueueDriverPtr driverHolder
	callbackQueueNamePtr   atomicString
)

type driverHolder struct {
	mu  sync.RWMutex
	drv Driver
}

func (h *driverHolder) get() Driver {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.drv
}

func (h *driverHolder) set(d Driver) {
	h.mu.Lock()
	h.drv = d
	h.mu.Unlock()
}

type atomicString struct {
	mu  sync.RWMutex
	val string
}

func (s *atomicString) get() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.val
}

func (s *atomicString) set(v string) {
	s.mu.Lock()
	s.val = v
	s.mu.Unlock()
}

// SetBatchCallbackQueue wires the queue driver used to enqueue
// BatchCallbackJob instances and the queue name to send them on.
// queueName empty means "default".
//
// Called from velocity's initQueue when an app boots. Apps that do not
// want cross-process callback delivery (single-process deployments) can
// leave this unset; closure callbacks still fire on the dispatcher
// process via the in-memory registry.
func SetBatchCallbackQueue(d Driver, queueName string) {
	callbackQueueDriverPtr.set(d)
	if queueName == "" {
		queueName = "default"
	}
	callbackQueueNamePtr.set(queueName)
}

// ResetBatchCallbackQueueForTest clears the wired driver. Tests that
// install one should call this in cleanup.
func ResetBatchCallbackQueueForTest() {
	callbackQueueDriverPtr.set(nil)
	callbackQueueNamePtr.set("")
}

func callbackQueueDriver() Driver { return callbackQueueDriverPtr.get() }
func callbackQueueName() string {
	if n := callbackQueueNamePtr.get(); n != "" {
		return n
	}
	return "default"
}

