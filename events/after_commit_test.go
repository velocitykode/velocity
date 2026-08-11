package events

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// afterCommitListener implements ShouldDispatchAfterCommit and records
// invocations so tests can assert whether the listener fired and how
// many times.
type afterCommitListener struct {
	defer_      bool
	invocations atomic.Int32
	lastEvent   atomic.Value // stores interface{} of last event seen
	handleErr   error
}

func (l *afterCommitListener) Handle(ctx context.Context, event interface{}) error {
	l.invocations.Add(1)
	l.lastEvent.Store(event)
	return l.handleErr
}

func (l *afterCommitListener) Async() bool                     { return false }
func (l *afterCommitListener) ShouldDispatchAfterCommit() bool { return l.defer_ }

// inlineListener does NOT implement ShouldDispatchAfterCommit. It is the
// behaviour control: a queue should not affect its dispatch.
type inlineListener struct {
	invocations atomic.Int32
}

func (l *inlineListener) Handle(ctx context.Context, event interface{}) error {
	l.invocations.Add(1)
	return nil
}

func (l *inlineListener) Async() bool { return false }

// TestDispatch_AfterCommitListener_FiresOnCommit asserts the happy path:
// a listener opting in via ShouldDispatchAfterCommit is queued during
// Dispatch and fires once FireAfterCommit runs.
func TestDispatch_AfterCommitListener_FiresOnCommit(t *testing.T) {
	d := NewDispatcher()
	listener := &afterCommitListener{defer_: true}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Listener must NOT have fired yet; the dispatch deferred it.
	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times before commit; want 0", got)
	}

	if pending := PendingAfterCommit(ctx); pending != 1 {
		t.Fatalf("PendingAfterCommit = %d; want 1", pending)
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times after commit; want 1", got)
	}

	// Queue is now finished: a follow-on Dispatch fires the listener inline.
	if HasAfterCommitQueue(ctx) {
		t.Fatal("HasAfterCommitQueue returned true after FireAfterCommit")
	}
}

// TestDispatch_AfterCommitListener_DroppedOnRollback asserts the rollback
// path: a listener opting in via ShouldDispatchAfterCommit is queued but
// never fires when DropAfterCommit runs instead of FireAfterCommit. This
// is the M-48 invariant: rolled-back transactions must not produce
// phantom side effects (read-model updates, outbox enqueues, email).
func TestDispatch_AfterCommitListener_DroppedOnRollback(t *testing.T) {
	d := NewDispatcher()
	listener := &afterCommitListener{defer_: true}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if pending := PendingAfterCommit(ctx); pending != 1 {
		t.Fatalf("PendingAfterCommit = %d; want 1", pending)
	}

	DropAfterCommit(ctx)

	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times after Drop; want 0", got)
	}

	if HasAfterCommitQueue(ctx) {
		t.Fatal("HasAfterCommitQueue returned true after DropAfterCommit")
	}
}

// TestDispatch_NonOptInListener_FiresInlineEvenInTx asserts the
// non-regression guarantee: listeners that do NOT implement
// ShouldDispatchAfterCommit must keep firing inline regardless of whether
// a queue is installed. Anything else would be a silent behaviour change
// for callers that adopted PrepareAfterCommit at the top of a request
// handler.
func TestDispatch_NonOptInListener_FiresInlineEvenInTx(t *testing.T) {
	d := NewDispatcher()
	listener := &inlineListener{}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Listener must have fired inline; the queue must NOT have absorbed it.
	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("inline listener fired %d times; want 1", got)
	}
	if pending := PendingAfterCommit(ctx); pending != 0 {
		t.Fatalf("PendingAfterCommit = %d; want 0 (non-opt-in listener)", pending)
	}
}

// TestDispatch_AfterCommitListener_FiresInlineOutsideTx asserts that an
// opt-in listener still fires inline when there is no after-commit queue
// on ctx (no PrepareAfterCommit, no Transaction). The opt-in only changes
// behaviour inside a transaction: outside, the listener fires immediately
// so the existing single-call semantics are preserved.
func TestDispatch_AfterCommitListener_FiresInlineOutsideTx(t *testing.T) {
	d := NewDispatcher()
	listener := &afterCommitListener{defer_: true}
	d.Listen("user.created", listener)

	// No PrepareAfterCommit call: ctx has no queue.
	ctx := context.Background()

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times; want 1 (no queue installed)", got)
	}
}

// TestDispatch_AfterCommitListener_OptedOutFiresInline asserts that
// ShouldDispatchAfterCommit returning false (per-instance opt-out) keeps
// the inline path. The interface is a per-call selector, not a marker.
func TestDispatch_AfterCommitListener_OptedOutFiresInline(t *testing.T) {
	d := NewDispatcher()
	listener := &afterCommitListener{defer_: false}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times; want 1 (opted out)", got)
	}
	if pending := PendingAfterCommit(ctx); pending != 0 {
		t.Fatalf("PendingAfterCommit = %d; want 0", pending)
	}
}

// TestDispatch_MixedListeners asserts the mixed case: when the same event
// has both opt-in and opt-out listeners, the opt-out listener fires
// inline and the opt-in listener is deferred. FireAfterCommit fires only
// the opt-in.
func TestDispatch_MixedListeners(t *testing.T) {
	d := NewDispatcher()
	deferred := &afterCommitListener{defer_: true}
	inline := &inlineListener{}
	d.Listen("user.created", deferred)
	d.Listen("user.created", inline)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	if got := inline.invocations.Load(); got != 1 {
		t.Fatalf("inline listener fired %d times; want 1", got)
	}
	if got := deferred.invocations.Load(); got != 0 {
		t.Fatalf("deferred listener fired %d times; want 0", got)
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if got := deferred.invocations.Load(); got != 1 {
		t.Fatalf("deferred listener fired %d times after commit; want 1", got)
	}
	if got := inline.invocations.Load(); got != 1 {
		t.Fatalf("inline listener fired %d times after commit (unchanged); want 1", got)
	}
}

// TestFireAfterCommit_DrainsInOrder asserts FireAfterCommit preserves the
// order in which tasks were enqueued so listeners that depend on each
// other (rare, but possible: an outbox writer expecting an upstream cache
// invalidation to land first) see deterministic delivery.
func TestFireAfterCommit_DrainsInOrder(t *testing.T) {
	var order []string
	ctx := PrepareAfterCommit(context.Background())
	for _, tag := range []string{"a", "b", "c"} {
		tag := tag
		EnqueueAfterCommit(ctx, func(context.Context) error {
			order = append(order, tag)
			return nil
		})
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("order = %v; want [a b c]", order)
	}
}

// TestFireAfterCommit_ContinuesAfterListenerError asserts a listener
// returning an error does not abort the drain: subsequent listeners still
// run and the final error joins every failure. The OPPOSITE behaviour
// (first-error-stops) would let one buggy listener mask a later listener
// the operator depends on for compliance logging.
func TestFireAfterCommit_ContinuesAfterListenerError(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())

	errBoom := errors.New("boom")
	var ran [3]bool
	for i := range ran {
		i := i
		EnqueueAfterCommit(ctx, func(context.Context) error {
			ran[i] = true
			if i == 1 {
				return errBoom
			}
			return nil
		})
	}

	err := FireAfterCommit(ctx)
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("FireAfterCommit error = %v; want errBoom", err)
	}

	for i, r := range ran {
		if !r {
			t.Errorf("task %d did not run", i)
		}
	}
}

// TestFireAfterCommit_RecoversFromPanic asserts a panicking listener does
// not bring down the drain.
func TestFireAfterCommit_RecoversFromPanic(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())

	var ranAfter bool
	EnqueueAfterCommit(ctx, func(context.Context) error {
		panic("listener boom")
	})
	EnqueueAfterCommit(ctx, func(context.Context) error {
		ranAfter = true
		return nil
	})

	err := FireAfterCommit(ctx)
	if err == nil {
		t.Fatal("FireAfterCommit returned nil; want panic-as-error")
	}
	if !ranAfter {
		t.Fatal("second task did not run after first panicked")
	}
}

// TestEnqueueAfterCommit_NoQueueReturnsFalse asserts the signal Dispatcher
// uses to fall through to inline fire: an unprepared ctx has no queue, so
// EnqueueAfterCommit returns false and the gate hands off to the inline /
// queue branches.
func TestEnqueueAfterCommit_NoQueueReturnsFalse(t *testing.T) {
	if EnqueueAfterCommit(context.Background(), func(context.Context) error { return nil }) {
		t.Fatal("EnqueueAfterCommit returned true on a ctx with no queue")
	}
}

// TestEnqueueAfterCommit_AfterFinishReturnsFalse asserts that once the
// queue has been drained, further Enqueue calls return false so the
// dispatcher gate collapses to inline. Otherwise a Dispatch in a
// post-commit finalizer would silently disappear.
func TestEnqueueAfterCommit_AfterFinishReturnsFalse(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())
	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}
	if EnqueueAfterCommit(ctx, func(context.Context) error { return nil }) {
		t.Fatal("EnqueueAfterCommit returned true on a finished queue")
	}
}

// TestEnqueueAfterCommit_DuringDrainReturnsFalse asserts that a listener
// invoked from FireAfterCommit which itself fires a Dispatch with an
// opt-in listener does NOT loop back into the queue. The re-entrant
// Dispatch should fall through to the inline branch instead so the
// drain makes forward progress and the re-entrant listener runs
// immediately rather than vanishing on a finished queue.
func TestEnqueueAfterCommit_DuringDrainReturnsFalse(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())

	var reentrant bool
	EnqueueAfterCommit(ctx, func(ctx context.Context) error {
		// Re-entrant enqueue from inside the drain.
		ok := EnqueueAfterCommit(ctx, func(context.Context) error {
			reentrant = true
			return nil
		})
		if ok {
			t.Error("re-entrant EnqueueAfterCommit returned true during drain")
		}
		return nil
	})

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}
	if reentrant {
		t.Error("re-entrant task ran; expected the re-entrant Enqueue to refuse")
	}
}

// TestPrepareAfterCommit_Idempotent pins the idempotence guarantee: a
// double call returns the same ctx so nested PrepareAfterCommit does not
// stack holders and confuse the orm wiring.
func TestPrepareAfterCommit_Idempotent(t *testing.T) {
	ctx1 := PrepareAfterCommit(context.Background())
	ctx2 := PrepareAfterCommit(ctx1)
	if ctx1 != ctx2 {
		t.Fatal("PrepareAfterCommit returned a different ctx on the second call")
	}
}

// TestInstallAfterCommitQueue_OwnerSemantics pins the owner contract used
// by orm.Manager.Transaction: the FIRST call installs the queue and
// returns a handle with Owner()==true; subsequent calls on the same ctx
// see the existing queue and return a handle with Owner()==false so
// nested transactions do not flush listeners belonging to the outermost
// commit.
func TestInstallAfterCommitQueue_OwnerSemantics(t *testing.T) {
	outerCtx, outerHandle := InstallAfterCommitQueue(context.Background())
	if !outerHandle.Owner() {
		t.Fatal("first InstallAfterCommitQueue returned Owner()=false")
	}
	_, innerHandle := InstallAfterCommitQueue(outerCtx)
	if innerHandle.Owner() {
		t.Fatal("nested InstallAfterCommitQueue returned Owner()=true; outer must retain ownership")
	}
}

// TestPrepareAfterCommit_FirstInstallClaimsAsOwner is the canonical M-48 F2
// regression: PrepareAfterCommit attaches an unclaimed queue, then the first
// InstallAfterCommitQueue (typically from orm.Manager.Transaction) MUST
// flip the claim and return owner=true. Without the claim flip the Install
// saw the prepared queue as already-nested, returned owner=false, and the
// Transaction wrapper silently never drained the queue: every queued
// listener went unfired on commit.
func TestPrepareAfterCommit_FirstInstallClaimsAsOwner(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())
	_, handle := InstallAfterCommitQueue(ctx)
	if !handle.Owner() {
		t.Fatal("first Install after PrepareAfterCommit returned Owner()=false; queue would be orphaned")
	}
}

// TestPrepareAfterCommit_NestedInstallAfterClaimIsNonOwner pins the second
// half of the F2 contract: once the first Install has claimed the
// prepared queue, a subsequent Install on the same ctx is nested and
// MUST return owner=false so the inner Transaction's commit / rollback
// boundary does not drain the outer owner's listeners.
func TestPrepareAfterCommit_NestedInstallAfterClaimIsNonOwner(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())
	_, outer := InstallAfterCommitQueue(ctx)
	if !outer.Owner() {
		t.Fatal("first Install returned Owner()=false")
	}
	_, inner := InstallAfterCommitQueue(ctx)
	if inner.Owner() {
		t.Fatal("second Install after claim returned Owner()=true; nested install must be non-owner")
	}
}

// TestPrepareAfterCommit_NestedInstallRespectsBaseline pins savepoint
// semantics on the F2 path: after the first Install claims the queue and
// enqueues task A, a nested Install captures baseline=1; the nested
// caller enqueues B; TruncateToBaseline on the nested handle drops B but
// keeps A; the owner's drain fires only A.
func TestPrepareAfterCommit_NestedInstallRespectsBaseline(t *testing.T) {
	ctx := PrepareAfterCommit(context.Background())
	_, owner := InstallAfterCommitQueue(ctx)
	if !owner.Owner() {
		t.Fatal("first Install returned Owner()=false")
	}

	var fired []string
	if !EnqueueAfterCommit(ctx, func(context.Context) error {
		fired = append(fired, "A")
		return nil
	}) {
		t.Fatal("owner EnqueueAfterCommit(A) returned false")
	}

	_, nested := InstallAfterCommitQueue(ctx)
	if nested.Owner() {
		t.Fatal("nested Install returned Owner()=true")
	}

	if !EnqueueAfterCommit(ctx, func(context.Context) error {
		fired = append(fired, "B")
		return nil
	}) {
		t.Fatal("nested EnqueueAfterCommit(B) returned false")
	}

	if got := PendingAfterCommit(ctx); got != 2 {
		t.Fatalf("PendingAfterCommit before truncate = %d; want 2", got)
	}

	nested.TruncateToBaseline()

	if got := PendingAfterCommit(ctx); got != 1 {
		t.Fatalf("PendingAfterCommit after nested truncate = %d; want 1 (only A survives)", got)
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if len(fired) != 1 || fired[0] != "A" {
		t.Fatalf("drained tasks = %v; want [A] (B was truncated)", fired)
	}
}

// TestDispatch_AfterCommitListener_UsesReplayContext asserts the listener
// observes the ctx passed to FireAfterCommit, not the (now stale)
// in-flight tx ctx. Listeners that read deadlines or trace IDs from ctx
// would otherwise see a closed-over value that no longer reflects
// post-commit reality.
func TestDispatch_AfterCommitListener_UsesReplayContext(t *testing.T) {
	type ctxKey struct{}

	d := NewDispatcher()
	var sawValue interface{}
	listener := &afterCommitListener{
		defer_:    true,
		handleErr: nil,
	}
	d.Listen("evt", listener)

	// Wrap Handle with a probe that reads from ctx.
	d.Off(d.listeners["evt"][0].id)
	d.Listen("evt", funcListener{
		handle: func(ctx context.Context, _ interface{}) error {
			sawValue = ctx.Value(ctxKey{})
			return nil
		},
		isAsync:     false,
		shouldDefer: true,
	})

	dispatchCtx := PrepareAfterCommit(context.Background())
	if err := d.Dispatch(dispatchCtx, "evt"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Commit-time ctx carries a key the dispatch ctx did not have.
	commitCtx := context.WithValue(dispatchCtx, ctxKey{}, "post-commit")
	if err := FireAfterCommit(commitCtx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	if sawValue != "post-commit" {
		t.Fatalf("listener saw ctx value %v; want post-commit (replay ctx)", sawValue)
	}
}

// funcListener wraps a plain function for inline construction in tests
// that need a one-off ShouldDispatchAfterCommit listener.
type funcListener struct {
	handle      func(ctx context.Context, event interface{}) error
	isAsync     bool
	shouldDefer bool
}

func (l funcListener) Handle(ctx context.Context, event interface{}) error {
	return l.handle(ctx, event)
}

func (l funcListener) Async() bool                     { return l.isAsync }
func (l funcListener) ShouldDispatchAfterCommit() bool { return l.shouldDefer }

// TestInstallAfterCommitQueue_NestedHandleTruncatesBaseline pins the M-48
// nested-rollback contract: a nested install captures the queue's current
// task count as a baseline; TruncateToBaseline rolls only inner-enqueued
// tasks back, leaving outer-enqueued tasks intact. Without this the
// outer commit fires inner work that was logically rolled back.
func TestInstallAfterCommitQueue_NestedHandleTruncatesBaseline(t *testing.T) {
	outerCtx, outerHandle := InstallAfterCommitQueue(context.Background())
	if !outerHandle.Owner() {
		t.Fatal("outer handle Owner()=false")
	}

	// Outer enqueues task A.
	if !EnqueueAfterCommit(outerCtx, func(context.Context) error { return nil }) {
		t.Fatal("outer EnqueueAfterCommit returned false")
	}

	// Nested install: baseline should be 1 (the outer A).
	innerCtx, innerHandle := InstallAfterCommitQueue(outerCtx)
	if innerHandle.Owner() {
		t.Fatal("inner handle Owner()=true; expected false on nested install")
	}

	// Inner enqueues task B.
	if !EnqueueAfterCommit(innerCtx, func(context.Context) error { return nil }) {
		t.Fatal("inner EnqueueAfterCommit returned false")
	}

	if got := PendingAfterCommit(outerCtx); got != 2 {
		t.Fatalf("PendingAfterCommit before truncate = %d; want 2", got)
	}

	// Inner rollback: truncate to baseline (1). Outer's A survives.
	innerHandle.TruncateToBaseline()

	if got := PendingAfterCommit(outerCtx); got != 1 {
		t.Fatalf("PendingAfterCommit after truncate = %d; want 1 (outer A survives)", got)
	}
}

// TestInstallAfterCommitQueue_OwnerHandleTruncateIsDrop asserts the
// owner-level convenience: TruncateToBaseline on a fresh owner handle
// (baseline=0) clears the entire queue, behaving like DropAfterCommit
// minus the finished-state transition. The owner caller should still
// prefer DropAfterCommit for clarity but the method is safe.
func TestInstallAfterCommitQueue_OwnerHandleTruncateIsDrop(t *testing.T) {
	ctx, handle := InstallAfterCommitQueue(context.Background())
	if !handle.Owner() {
		t.Fatal("owner handle Owner()=false")
	}

	for i := 0; i < 3; i++ {
		if !EnqueueAfterCommit(ctx, func(context.Context) error { return nil }) {
			t.Fatalf("EnqueueAfterCommit %d returned false", i)
		}
	}
	if got := PendingAfterCommit(ctx); got != 3 {
		t.Fatalf("PendingAfterCommit = %d; want 3", got)
	}

	handle.TruncateToBaseline()

	if got := PendingAfterCommit(ctx); got != 0 {
		t.Fatalf("PendingAfterCommit after owner truncate = %d; want 0", got)
	}
}

// TestInstallAfterCommitQueue_ZeroHandleSafeNoop asserts the zero-value
// AfterCommitHandle is safe to call: a caller that drops the handle on
// the floor (or uses a zero-value default) cannot panic the process.
func TestInstallAfterCommitQueue_ZeroHandleSafeNoop(t *testing.T) {
	var h AfterCommitHandle
	if h.Owner() {
		t.Fatal("zero handle Owner()=true")
	}
	// Must not panic.
	h.TruncateToBaseline()
}

// recordingQueue captures Push invocations so a test can assert a
// listener went through the queue branch instead of running inline.
type recordingQueue struct {
	pushed []recordingPush
}

type recordingPush struct {
	event    interface{}
	listener Listener
	delay    time.Duration
}

func (q *recordingQueue) Push(_ context.Context, event interface{}, listener Listener, delay time.Duration) error {
	q.pushed = append(q.pushed, recordingPush{event: event, listener: listener, delay: delay})
	return nil
}

// queueDeferListener implements BOTH Async and
// ShouldDispatchAfterCommit. Under a transaction, Dispatch must defer to
// the after-commit queue; at commit time, the replay must re-route
// through the queue dispatcher (not run the listener inline). Without
// the M-47 closure fix, the listener fires sync on the commit goroutine.
type queueDeferListener struct {
	invocations atomic.Int32
}

func (l *queueDeferListener) Handle(_ context.Context, _ interface{}) error {
	l.invocations.Add(1)
	return nil
}

func (l *queueDeferListener) Async() bool                     { return true }
func (l *queueDeferListener) ShouldDispatchAfterCommit() bool { return true }

// TestDispatch_AfterCommitListener_AsyncRoutesThroughQueueAtCommit
// closes the M-47 follow-up: a listener that opts into both
// after-commit AND queue must be PUSHED to the queue when the
// transaction commits, not invoked synchronously on the commit
// goroutine. Sync invocation silently breaks the listener's declared
// async semantics and blocks the orm wrapper return.
func TestDispatch_AfterCommitListener_AsyncRoutesThroughQueueAtCommit(t *testing.T) {
	d := NewDispatcher()
	q := &recordingQueue{}
	d.SetQueueDispatcher(q)

	listener := &queueDeferListener{}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())

	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Inside the tx: listener has NOT fired, nothing pushed yet (the
	// replay closure has not run; the queue Push happens at commit).
	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired %d times inside tx; want 0", got)
	}
	if len(q.pushed) != 0 {
		t.Fatalf("queue received %d pushes inside tx; want 0", len(q.pushed))
	}
	if pending := PendingAfterCommit(ctx); pending != 1 {
		t.Fatalf("PendingAfterCommit = %d; want 1", pending)
	}

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	// At commit: the replay closure ran, observed Async()==true,
	// and pushed to the queue. The listener body did NOT run on the
	// commit goroutine; the queue would invoke it asynchronously in
	// production.
	if got := listener.invocations.Load(); got != 0 {
		t.Fatalf("listener fired sync on commit goroutine (%d times); want 0", got)
	}
	if len(q.pushed) != 1 {
		t.Fatalf("queue received %d pushes after commit; want 1", len(q.pushed))
	}
	if q.pushed[0].event != "user.created" {
		t.Fatalf("queued event = %v; want user.created", q.pushed[0].event)
	}
	if q.pushed[0].listener != listener {
		t.Fatal("queued listener pointer mismatch")
	}
}

// TestDispatch_AfterCommitListener_AsyncFallsThroughWhenQueueUnwired
// pins the rare-race fallback: if the queue dispatcher is unwired
// between dispatch and commit, the replay closure falls back to inline
// invocation rather than silently dropping the listener.
func TestDispatch_AfterCommitListener_AsyncFallsThroughWhenQueueUnwired(t *testing.T) {
	d := NewDispatcher()
	q := &recordingQueue{}
	d.SetQueueDispatcher(q)

	listener := &queueDeferListener{}
	d.Listen("user.created", listener)

	ctx := PrepareAfterCommit(context.Background())
	if err := d.Dispatch(ctx, "user.created"); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Unwire the queue before commit.
	d.SetQueueDispatcher(nil)

	if err := FireAfterCommit(ctx); err != nil {
		t.Fatalf("FireAfterCommit returned error: %v", err)
	}

	// Listener fired inline because the queue was nil at replay time.
	if got := listener.invocations.Load(); got != 1 {
		t.Fatalf("listener fired %d times; want 1 (inline fallback)", got)
	}
	if len(q.pushed) != 0 {
		t.Fatalf("queue received %d pushes after unwire; want 0", len(q.pushed))
	}
}
