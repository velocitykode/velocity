package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/goroutine"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Conformance assertions: both dispatcher implementations satisfy the
// stdlib-only contract.Dispatcher closure. FakeDispatcher lives in fake.go;
// its assertion is placed here to avoid importing contract into that file.
var (
	_ contract.Dispatcher = (*DefaultDispatcher)(nil)
	_ contract.Dispatcher = (*FakeDispatcher)(nil)
)

// DefaultDispatcher is the default event dispatcher implementation
type DefaultDispatcher struct {
	mu        sync.RWMutex
	listeners map[string][]listenerEntry
	wildcards map[string][]listenerEntry
	queue     QueueDispatcher // Optional queue dispatcher for async events
	nextID    int             // Counter for generating listener IDs
	// keysByID holds every name or pattern a listener ID was registered
	// under (a []string key registers one ID under several), in
	// registration order, repeats kept, so Off removes it everywhere.
	keysByID map[int][]string

	// typed holds the listeners registered under an EventType key, in
	// registration order. They are matched against an event's Go type, not
	// its name.
	typed []typedEntry

	// resolvedCache memoizes the fully-resolved []Listener slice (exact,
	// wildcard and type matches) keyed by event name and Go type, so the
	// hot Dispatch path skips the per-call slice allocation and the scans of
	// d.wildcards and d.typed. It is a
	// sync.Map (security rule #3: a shared map needs its own protection)
	// rather than data guarded by d.mu so the common cache-hit path needs no
	// d.mu at all.
	//
	// It is bounded: every listener mutation (Listen, Off, Flush) empties
	// it under d.mu.Lock, and it holds at most maxResolvedEntries entries,
	// counted in cacheLen; past that a name is resolved on each dispatch
	// and not stored. Entries are stored only under d.mu.RLock, so none
	// built before a mutation survives it, and a removed listener is not
	// kept reachable. A long-lived dispatcher that sees unbounded distinct
	// event names (dynamic names matched by a wildcard) stays bounded.
	//
	// Each entry is tagged with the cacheEpoch under which it was built.
	// Every mutation bumps cacheEpoch under d.mu.Lock BEFORE it mutates
	// listener state, so a concurrent cache-hit dispatch (which bypasses
	// d.mu) holding an entry it loaded before the mutation finds it stale and
	// falls back to the locked resolve path -- where it blocks behind the
	// writer and sees the completed mutation. A dispatch starting after a
	// writer takes d.mu.Lock cannot fire a removed listener or miss a newly
	// added one.
	resolvedCache sync.Map // resolvedKey -> resolvedListeners
	cacheEpoch    atomic.Uint64
	cacheLen      atomic.Int64

	// failureReporter, when set, receives every dispatched event that
	// implements contract.FailureEvent, synchronously, before listener
	// fan-out. The framework wires it to ErrorHandler.Report at
	// bootstrap so background failures (failed jobs, scheduled tasks,
	// async listeners) reach the Reporter chain reliably even though
	// listener delivery may be asynchronous or best-effort.
	failureReporter func(ctx context.Context, event interface{}, err error)

	// reporting holds the goroutines currently inside a failureReporter
	// call; reportFailure consults it so a reporter that synchronously
	// re-dispatches a failure event cannot recurse through the bridge EVEN
	// IF it swaps in a fresh context (context.Background()), which the ctx
	// marker alone cannot catch.
	reporting goroutine.Set

	// detachedFailures, when set, records each detached delivery a
	// listener failed on (see SetDetachedFailureRecorder).
	detachedFailures atomic.Pointer[func(ctx context.Context, err error, event any)]
}

// listenerEntry wraps a Listener with an ID for tracking
type listenerEntry struct {
	id       int
	listener Listener
}

// typedEntry is a listener registered under an EventType key.
type typedEntry struct {
	id       int
	key      EventType
	listener Listener
}

// maxResolvedEntries bounds resolvedCache. Framework and application event
// names are a small fixed set in practice; the bound only matters for names
// built at run time.
const maxResolvedEntries = 1024

// resolvedKey is a resolvedCache key. The Go type is part of it because
// EventType listeners match by type, so two events that share a name can
// reach different listeners.
type resolvedKey struct {
	name string
	typ  reflect.Type
}

// resolvedListeners is a resolvedCache value: the memoized listener slice plus
// the cacheEpoch it was built under. A cache hit is only valid while its epoch
// matches the live cacheEpoch; a writer bumps the epoch before mutating, which
// invalidates every outstanding entry.
type resolvedListeners struct {
	epoch     uint64
	listeners []Listener
}

// QueueDispatcher handles queued event dispatching
type QueueDispatcher interface {
	Push(ctx context.Context, event interface{}, listener Listener, delay time.Duration) error
}

// NewDispatcher creates a new event dispatcher
func NewDispatcher() *DefaultDispatcher {
	return &DefaultDispatcher{
		listeners: make(map[string][]listenerEntry),
		wildcards: make(map[string][]listenerEntry),
		keysByID:  make(map[int][]string),
	}
}

// SetQueueDispatcher sets the queue dispatcher for async events
func (d *DefaultDispatcher) SetQueueDispatcher(qd QueueDispatcher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queue = qd
}

// failureReportedKey carries the failure event instance that has already
// been reported under this context. The marker is EVENT-IDENTITY scoped: it
// suppresses only a re-dispatch of that SAME event instance (a listener or
// reporter looping the event back with the ctx it received). A DIFFERENT
// failure event dispatched with that ctx still reports normally; blanket
// suppression of everything under a marked ctx would silently swallow
// listener-originated terminal failures. The bridge-internal fallback
// re-dispatches do not rely on this marker at all; they skip the bridge
// deterministically via the detached flag on dispatch/dispatchNow.
type failureReportedKey struct{}

// SetFailureReporter installs the bridge that forwards FailureEvent
// dispatches to the error Reporter chain. Pass nil to disable.
// Safe for concurrent use with Dispatch.
func (d *DefaultDispatcher) SetFailureReporter(fn func(ctx context.Context, event interface{}, err error)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.failureReporter = fn
}

// reportFailure forwards event to the failure reporter when event
// implements contract.FailureEvent. It runs synchronously on the
// dispatching goroutine (reliable, unlike best-effort listener delivery).
// Every public dispatch path (Dispatch, DispatchNow, DispatchAsync,
// DispatchAfter, Until, and QueueIntegratedDispatcher.Dispatch) calls it
// at its entry point so a FailureEvent is reported at the point of
// dispatch regardless of how it is routed.
//
// Two guards keep "exactly once" honest without swallowing real failures:
//
//  1. Event-identity ctx marker: the returned context records THIS event as
//     reported. Callers continue dispatching with it, so a listener or
//     reporter that loops the same event instance back with the ctx it
//     received skips the report, while a different failure event dispatched
//     downstream with the same ctx still reports. (The bridge-internal
//     fallback re-dispatches skip the bridge deterministically via the
//     detached flag instead and do not depend on this identity check.)
//
//  2. Per-goroutine reporter guard: while a reporter call is in flight on a
//     goroutine, any failure event dispatched synchronously from inside it is
//     bridged-to-listeners only, regardless of the context the reporter
//     supplies. The ctx marker cannot catch a reporter that re-dispatches
//     with context.Background(); this guard can, and it is released even if
//     the reporter panics. A reporter that hands the event to ANOTHER
//     goroutine with a fresh context is the one loop neither guard can see;
//     reporters must propagate the ctx they were given when they re-dispatch
//     asynchronously.
func (d *DefaultDispatcher) reportFailure(ctx context.Context, event interface{}) context.Context {
	fe, ok := event.(contract.FailureEvent)
	if !ok {
		return ctx
	}
	if sameFailureEvent(ctx.Value(failureReportedKey{}), event) {
		return ctx
	}

	gid := goroutine.ID()
	if d.reporting.Contains(gid) {
		return ctx
	}

	d.mu.RLock()
	report := d.failureReporter
	d.mu.RUnlock()
	if report == nil {
		return ctx
	}
	err := fe.FailureError()
	if err == nil {
		return ctx
	}

	marked := context.WithValue(ctx, failureReportedKey{}, event)

	d.reporting.Enter(gid)
	defer d.reporting.Leave(gid)
	report(marked, event, err)
	return marked
}

// sameFailureEvent reports whether the ctx marker value records the same
// event instance as the one being dispatched. Interface equality on an
// uncomparable dynamic type panics, so uncomparable events are treated as
// distinct, which errs on the side of not losing a failure. The bridge-
// internal fallback paths do not depend on this comparison (they skip the
// bridge deterministically via the detached flag on dispatch/dispatchNow);
// the marker only dedupes a LISTENER or REPORTER re-dispatching the same
// event instance with the ctx it received, where pointer events compare by
// identity and an uncomparable value event would at worst re-report.
func sameFailureEvent(marker, event interface{}) bool {
	if marker == nil || event == nil {
		return false
	}
	mt, et := reflect.TypeOf(marker), reflect.TypeOf(event)
	if mt != et || !mt.Comparable() {
		return false
	}
	return marker == event
}

// Listen adds a listener for the events key selects: an EventType from
// OfType, a name or pattern, several names ([]string), or an event value
// (its name); the package documentation describes each under "Listening".
// Multiple listeners may be registered for the same event (append semantics
// -- duplicates are intentional, not an error). Returns a listener ID that
// can be used with Off() to unregister the listener. Panics with
// *contract.RegistrationError if listener is nil or key is a zero EventType.
func (d *DefaultDispatcher) Listen(key interface{}, listener Listener) int {
	if listener == nil {
		panic(contract.NewRegistrationError("events", "nil listener"))
	}
	if k, ok := key.(EventType); ok && k.matches == nil {
		panic(contract.NewRegistrationError("events", "zero EventType key; build one with OfType"))
	}
	// An event value's name is user code (its Name method): resolve it
	// before taking the lock, so a Name that dispatches cannot deadlock.
	var valueName string
	switch key.(type) {
	case EventType, string, []string:
	default:
		valueName = d.getEventName(key)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	// Invalidate the resolved cache before touching listener state so a
	// concurrent fast-path dispatch cannot keep using a pre-mutation slice.
	d.invalidateResolved()

	// Generate a unique ID for this listener
	d.nextID++
	id := d.nextID

	// Handle different event types
	switch e := key.(type) {
	case EventType:
		d.typed = append(d.typed, typedEntry{id: id, key: e, listener: listener})
	case string:
		d.addListener(e, listener, id)
	case []string:
		for _, event := range e {
			d.addListener(event, listener, id)
		}
	default:
		// An event value: listen under its name.
		d.addListener(valueName, listener, id)
	}

	return id
}

// addListener adds a listener to the appropriate map with the given ID
func (d *DefaultDispatcher) addListener(event string, listener Listener, id int) {
	entry := listenerEntry{id: id, listener: listener}

	// Check if it's a wildcard pattern
	if strings.Contains(event, "*") {
		d.wildcards[event] = append(d.wildcards[event], entry)
	} else {
		d.listeners[event] = append(d.listeners[event], entry)
	}

	// Track every key of the ID for removal
	d.keysByID[id] = append(d.keysByID[id], event)
}

// invalidateResolved empties the resolved cache before a listener
// mutation. Caller must hold d.mu.Lock.
func (d *DefaultDispatcher) invalidateResolved() {
	d.cacheEpoch.Add(1)
	d.resolvedCache.Clear()
	d.cacheLen.Store(0)
}

// Off removes a listener by its ID, under every name and pattern it was
// registered for. Returns true if the listener was found and removed,
// false otherwise.
func (d *DefaultDispatcher) Off(id int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	keys, exists := d.keysByID[id]
	if !exists {
		return d.removeTyped(id)
	}

	// Invalidate the resolved cache before mutating listener state.
	d.invalidateResolved()

	for _, key := range keys {
		d.removeEntries(key, id)
	}
	delete(d.keysByID, id)
	return true
}

// removeEntries removes every entry of listener id registered under key, a
// name or a pattern. Caller must hold d.mu.Lock.
func (d *DefaultDispatcher) removeEntries(key string, id int) {
	m := d.listeners
	if strings.Contains(key, "*") {
		m = d.wildcards
	}
	kept := m[key][:0]
	for _, entry := range m[key] {
		if entry.id != id {
			kept = append(kept, entry)
		}
	}
	if len(kept) == 0 {
		delete(m, key)
		return
	}
	m[key] = kept
}

// forgetKey drops key from the keys listener id was registered under, after
// Flush removed its entries there. Caller must hold d.mu.Lock.
func (d *DefaultDispatcher) forgetKey(id int, key string) {
	keys := d.keysByID[id]
	kept := keys[:0]
	for _, k := range keys {
		if k != key {
			kept = append(kept, k)
		}
	}
	if len(kept) == 0 {
		delete(d.keysByID, id)
		return
	}
	d.keysByID[id] = kept
}

// removeTyped removes the EventType listener with the given ID. Caller must
// hold d.mu.Lock.
func (d *DefaultDispatcher) removeTyped(id int) bool {
	for i, entry := range d.typed {
		if entry.id == id {
			// Invalidate the resolved cache before mutating listener state.
			d.invalidateResolved()
			d.typed = append(d.typed[:i], d.typed[i+1:]...)
			return true
		}
	}
	return false
}

// Subscribe registers an event subscriber
func (d *DefaultDispatcher) Subscribe(subscriber Subscriber) {
	subscriber.Subscribe(d)
}

// Dispatch fires an event to all registered listeners.
// Listeners that return true from Async are dispatched via the queue;
// all others are processed synchronously. Returns an error if event is nil.
//
// After-commit gating: a listener that implements
// ShouldDispatchAfterCommit and returns true is queued onto the
// after-commit task list attached to ctx (events.PrepareAfterCommit +
// orm.Manager.Transaction). The listener fires only if the surrounding
// transaction commits and is dropped on rollback. Outside a transaction
// (no queue on ctx) the listener fires inline so behaviour is unchanged
// for callers that have not wired the orm hook. Non-opt-in listeners
// always fire inline (or via the queue if Async is true) regardless
// of the after-commit queue state.
func (d *DefaultDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	return d.dispatch(ctx, event, false)
}

// dispatch is the Dispatch core. detached marks a delivery that runs after
// the public call returned, with no caller left waiting on it (DispatchAfter's
// no-queue timer fallback, and the failure events a detached delivery
// dispatches): the public call already reported a FailureEvent at the point
// of dispatch, so the failure-reporter bridge is skipped, which keeps
// "exactly once" DETERMINISTIC for every event value, comparable or not,
// instead of depending on the ctx marker's identity comparison; and each
// listener's error or recovered panic, which no caller would receive, is
// dispatched as its own AsyncFailed (see dispatchListenerFailure) and the
// delivery is recorded once (see SetDetachedFailureRecorder). Every public
// entry point passes false.
func (d *DefaultDispatcher) dispatch(ctx context.Context, event interface{}, detached bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil {
		return fmt.Errorf("events: cannot dispatch nil event")
	}
	if !detached {
		ctx = d.reportFailure(ctx, event)
	}
	d.mu.RLock()
	q := d.queue
	d.mu.RUnlock()
	// deferred collects this dispatch's after-commit listeners (see
	// deferUntilCommit); it stays nil unless one meets a live queue.
	var deferred *afterCommitDelivery
	deliver := func(listener Listener) error {
		// After-commit gate runs FIRST: a listener that opts into
		// post-commit delivery should never reach the queue or the
		// inline branch while the transaction is still in flight.
		// deferUntilCommit returns false when no queue is installed
		// or the queue has already drained, which collapses the gate
		// into the existing inline / queue branches below.
		if ac, ok := listener.(ShouldDispatchAfterCommit); ok && ac.ShouldDispatchAfterCommit() {
			if d.deferUntilCommit(ctx, event, &deferred, listener, d.replayListener) {
				return nil
			}
			// Fall through: no queue installed (no transaction) or
			// the queue already drained. The listener fires inline
			// just like a non-opt-in listener would.
		}
		if listener.Async() && q != nil {
			if err := q.Push(ctx, event, listener, 0); err != nil {
				return errchain.Errorf("failed to queue listener: %w", err)
			}
			return nil
		}
		return d.handleListener(ctx, event, listener)
	}
	if detached {
		d.deliverDetached(ctx, event, deliver)
		return nil
	}
	return d.dispatchToListeners(event, deliver)
}

// replayListener delivers listener, an after-commit listener of event, at
// commit time under the commit-time ctx, so it sees post-transaction
// values. Async and the live queue are re-checked: a listener that opts
// into BOTH after-commit AND queueing must still take the queue branch
// when the transaction lands, instead of running synchronously on the
// commit goroutine and silently changing its declared async semantics.
func (d *DefaultDispatcher) replayListener(ctx context.Context, event interface{}, listener Listener) error {
	if listener.Async() {
		d.mu.RLock()
		q := d.queue
		d.mu.RUnlock()
		if q != nil {
			if err := q.Push(ctx, event, listener, 0); err != nil {
				return errchain.Errorf("failed to queue listener: %w", err)
			}
			return nil
		}
		// Queue was unwired between dispatch and commit (rare). Fall
		// through to inline so the listener still runs rather than
		// silently disappearing.
	}
	return d.handleListener(ctx, event, listener)
}

// afterCommitDelivery is the part of one dispatch deferred to the commit
// of the surrounding transaction: the dispatch's after-commit listeners,
// delivered by one after-commit task. mu guards listeners and closed, for
// a dispatch racing the commit on another goroutine.
type afterCommitDelivery struct {
	mu        sync.Mutex
	listeners []Listener
	closed    bool
}

// deferUntilCommit defers listener, an after-commit listener of event, to
// the commit of the transaction ctx carries, as part of *group, the
// deferred part of this dispatch: the first listener creates the group and
// enqueues its one task, later ones join it. It reports false when there
// is no live after-commit queue (no transaction, or it already committed)
// or the group was already delivered: the listener then runs now, as a
// listener that does not wait for the commit would.
//
// At commit the task delivers each listener with replay, contained (see
// deliverContained), and returns their failures joined, so the
// transaction returns them. The deferred part is its own delivery, whose
// failure no dispatch caller receives: it is handed once, with the
// failures joined, to the recorder SetDetachedFailureRecorder installed.
func (d *DefaultDispatcher) deferUntilCommit(ctx context.Context, event interface{}, group **afterCommitDelivery, listener Listener, replay func(context.Context, interface{}, Listener) error) bool {
	if g := *group; g != nil {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.closed {
			return false
		}
		g.listeners = append(g.listeners, listener)
		return true
	}
	g := &afterCommitDelivery{listeners: []Listener{listener}}
	if !EnqueueAfterCommit(ctx, func(commitCtx context.Context) error {
		return d.deliverAfterCommit(commitCtx, event, g, replay)
	}) {
		return false
	}
	*group = g
	return true
}

// deliverAfterCommit delivers g, the deferred part of a dispatch of event,
// at commit (see deferUntilCommit).
func (d *DefaultDispatcher) deliverAfterCommit(ctx context.Context, event interface{}, g *afterCommitDelivery, replay func(context.Context, interface{}, Listener) error) error {
	g.mu.Lock()
	g.closed = true
	listeners := g.listeners
	g.mu.Unlock()

	var errs []error
	for _, listener := range listeners {
		if err := deliverContained(func(l Listener) error { return replay(ctx, event, l) }, listener); err != nil {
			errs = append(errs, err)
		}
	}
	err := errors.Join(errs...)
	if err != nil {
		if record := d.detachedFailures.Load(); record != nil {
			containDetached(event, func() { (*record)(ctx, err, event) })
		}
	}
	return err
}

// dispatchLater delivers event, which a public call accepted and returned
// for before its delivery (a debounced or coalesced dispatch), under ctx.
// The failure-report bridge sees a FailureEvent now, when it is delivered,
// so an event debounced away is never reported; and the delivery is
// detached (see dispatch): each listener's error or recovered panic, which
// no caller would receive, is dispatched as its own AsyncFailed and
// reported, instead of being lost.
func (d *DefaultDispatcher) dispatchLater(ctx context.Context, event interface{}) {
	if event == nil {
		return
	}
	// A detached dispatch returns an error only for a nil event, excluded
	// above: every listener failure became an AsyncFailed and was recorded.
	_ = d.dispatch(d.reportDetached(ctx, event), event, true)
}

// DispatchNow fires an event synchronously to all listeners.
func (d *DefaultDispatcher) DispatchNow(ctx context.Context, event interface{}) error {
	return d.dispatchNow(ctx, event, false)
}

// dispatchNow is the DispatchNow core; see dispatch for the detached flag.
// DispatchAsync's no-queue goroutine fallback passes true: the public
// DispatchAsync call already reported synchronously at the point of
// dispatch, and no caller waits on the goroutine.
func (d *DefaultDispatcher) dispatchNow(ctx context.Context, event interface{}, detached bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !detached {
		ctx = d.reportFailure(ctx, event)
	}
	deliver := func(listener Listener) error {
		return d.handleListener(ctx, event, listener)
	}
	if detached {
		d.deliverDetached(ctx, event, deliver)
		return nil
	}
	return d.dispatchToListeners(event, deliver)
}

// deliverDetached delivers event under ctx to each of its listeners with
// deliver, for a detached delivery: a listener's error or recovered panic
// has no caller left to return to, so it is dispatched as that listener's
// AsyncFailed (see dispatchListenerFailure), and when any listener failed
// the delivery is handed, once, with the listeners' failures joined, to
// the recorder SetDetachedFailureRecorder installed.
//
// It runs on a goroutine no caller waits on (a timer, a debounce or
// coalesce callback, the no-queue DispatchAsync goroutine), so nothing
// user code does may escape it: each listener's delivery is contained
// (see deliverContained), and a panic before any listener runs (the
// event's Name while its listeners are resolved) is the delivery's
// failure, recorded once with no AsyncFailed, since no listener failed.
func (d *DefaultDispatcher) deliverDetached(ctx context.Context, event interface{}, deliver func(Listener) error) {
	err := d.dispatchDetached(ctx, event, deliver)
	if err == nil {
		return
	}
	if record := d.detachedFailures.Load(); record != nil {
		containDetached(event, func() { (*record)(ctx, err, event) })
	}
}

// dispatchDetached is deliverDetached's fan-out: it returns the listeners'
// failures joined, or the recovered panic that ended the fan-out before
// any listener ran.
func (d *DefaultDispatcher) dispatchDetached(ctx context.Context, event interface{}, deliver func(Listener) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	// The name is resolved once, with the listeners: a listener's failure
	// is reported under it, and the event's Name is not called again.
	name, listeners := d.resolveListeners(event)
	return deliverEach(listeners, func(listener Listener) error {
		err := deliverContained(deliver, listener)
		if err != nil {
			d.dispatchListenerFailure(ctx, name, event, listener, err)
		}
		return err
	})
}

// detachedPanicMessage is the fallback line written for a panic contained
// on a detached delivery (see containDetached).
const detachedPanicMessage = "velocity/events: failure reporting panicked on a detached delivery"

// containDetached runs fn, a call into failure reporting (the recorder or
// the failure-report bridge) during a detached delivery of event, on the
// goroutine delivering it: a timer, a debounce or coalesce callback, or
// the no-queue DispatchAsync goroutine. No caller is left to receive a
// panic there, so a panic in fn is contained and written through the
// framework's fallback logger: it never ends the process, and never skips
// the rest of the delivery (the other listeners, their AsyncFailed and the
// recorder).
func containDetached(event interface{}, fn func()) {
	defer func() {
		if p := recover(); p != nil {
			// Formatting a panic value can panic in turn (an error whose
			// Error method panics); fallbacklog.Write contains that too.
			fallbacklog.Write(nil, func(l contract.Logger) {
				l.Error(detachedPanicMessage, "event", eventemit.EventName(event), "panic", errchain.Sprint(p))
			})
		}
	}()
	fn()
}

// reportDetached is reportFailure for a detached delivery: a failure
// reporter that panics is contained (see containDetached), and ctx is
// returned unmarked in that case.
func (d *DefaultDispatcher) reportDetached(ctx context.Context, event interface{}) (reported context.Context) {
	reported = ctx
	containDetached(event, func() { reported = d.reportFailure(ctx, event) })
	return reported
}

// SetDetachedFailureRecorder installs fn as the recorder of detached
// deliveries that failed: the no-queue fallbacks of DispatchAsync and
// DispatchAfter, the later deliveries of a debouncing or coalescing
// dispatcher, and the delivery of an AsyncFailed. It also records the
// part of a dispatch deferred to a transaction's commit (its
// ShouldDispatchAfterCommit listeners), once however many of them failed;
// no AsyncFailed is dispatched for those, as the commit returns their
// failure to the transaction's caller. No caller receives such
// a delivery's result, so fn is called once per delivery a listener
// failed on (however many did), with the listeners' failures joined, on
// the goroutine that delivered it, after each failure was dispatched as
// its AsyncFailed and reported. The unit differs on purpose: each failed
// listener is reported once (its AsyncFailed), and the delivery is recorded
// once. The framework installs the app's failure policy here, which counts
// the delivery once and hands it to the failure hook once. A panic in fn is
// contained and written through the framework's fallback logger: no caller
// is left to receive it. nil removes it. Safe for concurrent use with
// dispatching.
func (d *DefaultDispatcher) SetDetachedFailureRecorder(fn func(ctx context.Context, err error, event any)) {
	if fn == nil {
		d.detachedFailures.Store(nil)
		return
	}
	d.detachedFailures.Store(&fn)
}

// dispatchListenerFailure dispatches the AsyncFailed for listener, which
// failed with err during a detached delivery of event, named name, under
// ctx. The
// failure is reported through the failure-report bridge here, once, under
// ctx, which carries the caller's trace IDs; then the AsyncFailed is
// delivered to its own listeners detached, so a listener of AsyncFailed
// that fails in turn is reported as well. A failure while delivering an
// AsyncFailed is reported but not dispatched again, so a listener that
// fails on every event cannot loop.
func (d *DefaultDispatcher) dispatchListenerFailure(ctx context.Context, name string, event interface{}, listener Listener, err error) {
	failed := newAsyncFailed(ctx, name, listener, err)
	ctx = d.reportDetached(ctx, failed)
	if _, nested := event.(*AsyncFailed); nested {
		return
	}
	_ = d.dispatch(ctx, failed, true)
}

// DispatchAsync fires an event asynchronously via the queue.
// Falls back to a panic-safe goroutine (async.Go) if no queue is configured;
// there, a listener that returns an error or panics has no caller to return
// to, so the dispatcher dispatches an AsyncFailed for it, which the
// failure-report bridge reports.
func (d *DefaultDispatcher) DispatchAsync(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Report synchronously at the point of dispatch, before the event is
	// queued or detached onto a goroutine.
	ctx = d.reportFailure(ctx, event)
	d.mu.RLock()
	q := d.queue
	d.mu.RUnlock()
	if q == nil {
		// Detach from request lifetime so the goroutine can outlive the
		// caller, while still preserving values via context.WithoutCancel.
		// detached: the failure was already reported above, and skipping
		// the bridge here is deterministic and does not depend on the ctx
		// marker's identity comparison (uncomparable events included); each
		// listener's failure is dispatched as an AsyncFailed.
		bgCtx := context.WithoutCancel(ctx)
		async.Go(func() {
			_ = d.dispatchNow(bgCtx, event, true)
		})
		return nil
	}

	return d.dispatchToListeners(event, func(listener Listener) error {
		if err := q.Push(ctx, event, listener, 0); err != nil {
			return errchain.Errorf("failed to queue listener: %w", err)
		}
		return nil
	})
}

// DispatchAfter fires an event after a delay.
// Falls back to a timer if no queue is configured; there, a listener that
// returns an error or panics has no caller to return to, so the dispatcher
// dispatches an AsyncFailed for it, which the failure-report bridge
// reports.
func (d *DefaultDispatcher) DispatchAfter(ctx context.Context, event interface{}, delay time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Report synchronously NOW, not after the delay: the failure exists at
	// the point of dispatch.
	ctx = d.reportFailure(ctx, event)
	d.mu.RLock()
	q := d.queue
	d.mu.RUnlock()
	if q == nil {
		bgCtx := context.WithoutCancel(ctx)
		// detached: already reported above; the deterministic skip replaces
		// reliance on the ctx marker's identity comparison, which cannot
		// dedupe uncomparable event values, and each listener's failure is
		// dispatched as an AsyncFailed.
		time.AfterFunc(delay, func() {
			_ = d.dispatch(bgCtx, event, true)
		})
		return nil
	}

	return d.dispatchToListeners(event, func(listener Listener) error {
		if err := q.Push(ctx, event, listener, delay); err != nil {
			return errchain.Errorf("failed to queue delayed listener: %w", err)
		}
		return nil
	})
}

// Until dispatches events until the first non-nil return.
//
// Each listener invocation is wrapped in a panic-recovery shim (see
// [DefaultDispatcher.safeInvokeForUntil]) so a panicking listener cannot
// unwind into the caller. A recovered panic is converted to an error via
// panicerr.FromRecovered and treated like a normal listener error: Until
// short-circuits and returns the error so callers (and the recovery
// listener pipeline shared with processListener) observe a complete chain
// rather than a vanished panic value.
func (d *DefaultDispatcher) Until(ctx context.Context, event interface{}) (interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = d.reportFailure(ctx, event)
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		result, err := d.safeInvokeForUntil(ctx, event, listener)
		if err != nil || result != nil {
			return result, err
		}
	}

	return nil, nil
}

// safeInvokeForUntil routes a single listener invocation for [Until]
// through the same recover-wrapped path that [processListener] uses. The
// recover block converts a panic into an error via panicerr.FromRecovered
// so callers see a typed *panicerr.Error in the error chain instead of
// the panic unwinding through the dispatcher into the caller's stack.
//
// A listener that implements HandleWithResult (used by Until's
// short-circuit semantics) is invoked through that method; everything
// else falls back to the regular Listener.Handle path. The shape mirrors
// processListener so the two recover blocks stay aligned if either is
// extended (e.g. ShouldHandle gating).
func (d *DefaultDispatcher) safeInvokeForUntil(ctx context.Context, event interface{}, listener Listener) (result interface{}, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()

	if handler, ok := listener.(interface {
		HandleWithResult(ctx context.Context, event interface{}) (interface{}, error)
	}); ok {
		return handler.HandleWithResult(ctx, event)
	}
	return nil, listener.Handle(ctx, event)
}

// Flush removes all listeners for an event
func (d *DefaultDispatcher) Flush(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Invalidate the resolved cache before mutating listener state.
	d.invalidateResolved()

	// A listener also registered under other keys stays there, and Off
	// still finds it.
	for _, entry := range d.listeners[event] {
		d.forgetKey(entry.id, event)
	}
	delete(d.listeners, event)

	// Also remove matching wildcards
	for pattern, entries := range d.wildcards {
		if matchesPattern(event, pattern) {
			for _, entry := range entries {
				d.forgetKey(entry.id, pattern)
			}
			delete(d.wildcards, pattern)
		}
	}
}

// Forget removes all listeners
func (d *DefaultDispatcher) Forget(event string) {
	d.Flush(event)
}

// HasListeners checks if an event has listeners
func (d *DefaultDispatcher) HasListeners(event interface{}) bool {
	return len(d.getListenersForEvent(event)) > 0
}

// GetListeners returns all listeners for an event.
//
// The returned slice is a copy: getListenersForEvent shares its result with
// the resolved-listener cache and with future dispatches, so a public caller
// that reorders or replaces elements must not be able to corrupt dispatch
// state. The internal hot path uses getListenersForEvent directly and treats
// the slice as read-only.
func (d *DefaultDispatcher) GetListeners(event interface{}) []Listener {
	internal := d.getListenersForEvent(event)
	out := make([]Listener, len(internal))
	copy(out, internal)
	return out
}

// getListenersForEvent retrieves all listeners for an event.
//
// The fully-resolved slice is memoized in resolvedCache keyed by event name
// and Go type, so the common exact-match path returns the cached slice with
// no map scan and no per-dispatch allocation. On a miss it builds the slice
// once (exact, wildcard and type matches) under d.mu.RLock and stores it. Callers must treat the
// returned slice as read-only: it is shared with the cache. The only in-place
// mutator, PriorityDispatcher.getListenersForEvent, clones before sorting.
func (d *DefaultDispatcher) getListenersForEvent(event interface{}) []Listener {
	_, listeners := d.resolveListeners(event)
	return listeners
}

// resolveListeners returns event's name and its listeners (see
// getListenersForEvent).
func (d *DefaultDispatcher) resolveListeners(event interface{}) (string, []Listener) {
	eventName := d.getEventName(event)
	cacheKey := resolvedKey{name: eventName, typ: reflect.TypeOf(event)}

	epoch := d.cacheEpoch.Load()

	// Fast path: no d.mu, no map scan, no alloc. The epoch tag rejects any
	// entry built before an in-progress or completed mutation, so a hit can
	// never return a pre-mutation slice once a writer has bumped the epoch.
	if cached, ok := d.resolvedCache.Load(cacheKey); ok {
		if entry := cached.(resolvedListeners); entry.epoch == epoch {
			return eventName, entry.listeners
		}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	// Re-read the epoch under the lock. No writer can be mid-mutation while we
	// hold RLock, so this value is stable for the build+store below.
	epoch = d.cacheEpoch.Load()

	// Re-check under the lock: a concurrent miss may have populated it.
	if cached, ok := d.resolvedCache.Load(cacheKey); ok {
		if entry := cached.(resolvedListeners); entry.epoch == epoch {
			return eventName, entry.listeners
		}
	}

	// Pre-compute capacity to avoid repeated slice growth
	capacity := len(d.listeners[eventName])
	for pattern, entries := range d.wildcards {
		if matchesPattern(eventName, pattern) {
			capacity += len(entries)
		}
	}
	for _, entry := range d.typed {
		if entry.key.matches(event) { //lock-held-ok: OfType's type assertion, no user code
			capacity++
		}
	}

	result := make([]Listener, 0, capacity)

	// Get exact match listeners
	if entries, ok := d.listeners[eventName]; ok {
		for _, entry := range entries {
			result = append(result, entry.listener)
		}
	}

	// Get wildcard listeners
	for pattern, entries := range d.wildcards {
		if matchesPattern(eventName, pattern) {
			for _, entry := range entries {
				result = append(result, entry.listener)
			}
		}
	}

	// Get EventType listeners
	for _, entry := range d.typed {
		if entry.key.matches(event) { //lock-held-ok: OfType's type assertion, no user code
			result = append(result, entry.listener)
		}
	}

	d.storeResolved(cacheKey, resolvedListeners{epoch: epoch, listeners: result})
	return eventName, result
}

// storeResolved stores entry under key unless the cache holds
// maxResolvedEntries entries already. Caller must hold d.mu.RLock, so no
// mutation can clear the cache between the check and the store.
func (d *DefaultDispatcher) storeResolved(key resolvedKey, entry resolvedListeners) {
	for n := d.cacheLen.Load(); n < maxResolvedEntries; n = d.cacheLen.Load() {
		if !d.cacheLen.CompareAndSwap(n, n+1) {
			continue
		}
		if _, loaded := d.resolvedCache.LoadOrStore(key, entry); loaded {
			// A concurrent resolve of the same key stored it first.
			d.cacheLen.Add(-1)
		}
		return
	}
}

// getEventName extracts the event name from various types.
func (d *DefaultDispatcher) getEventName(event interface{}) string {
	return resolveEventName(event)
}

// matchesPattern reports whether the event name matches a listener key: the
// key itself when it holds no "*", otherwise the pattern the package
// documentation describes under "Listening". It is the package's one string
// matcher; the dispatchers and the fake's assertions all go through it.
func matchesPattern(name, pattern string) bool {
	prefix, suffix, isPattern := strings.Cut(pattern, "*")
	if !isPattern {
		return name == pattern
	}
	if strings.Contains(suffix, "*") {
		return false
	}
	return len(name) >= len(prefix)+len(suffix) &&
		strings.HasPrefix(name, prefix) &&
		strings.HasSuffix(name, suffix)
}

// dispatchToListeners resolves listeners for an event and applies fn to each.
// Errors from individual listeners are aggregated with errors.Join so a single
// failure does not mask subsequent problems and callers can inspect every
// listener result. Each listener's delivery is contained (see
// deliverContained): a panic in fn fails that listener only.
func (d *DefaultDispatcher) dispatchToListeners(event interface{}, fn func(Listener) error) error {
	return deliverEach(d.getListenersForEvent(event), fn)
}

// deliverEach applies fn to each listener, contained (see
// deliverContained), and returns their failures joined.
func deliverEach(listeners []Listener, fn func(Listener) error) error {
	var errs []error
	for _, listener := range listeners {
		if err := deliverContained(fn, listener); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// deliverContained delivers to listener with deliver, returning a panic in
// it as the typed panic error. It is the one containment of a listener's
// delivery: everything a delivery calls on the listener (Async,
// ShouldDispatchAfterCommit, ShouldHandle, Handle) and the queue push it
// may make is user code, and a panic in any of them fails that listener
// only, on every path (a synchronous dispatch, a queue push, a timer or a
// detached goroutine).
func deliverContained(deliver func(Listener) error, listener Listener) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	return deliver(listener)
}

// handleListener executes a listener: ShouldHandle, then Handle. It does
// not recover; its callers run it inside deliverContained.
func (d *DefaultDispatcher) handleListener(ctx context.Context, event interface{}, listener Listener) error {
	// Check if listener should handle this event
	if handler, ok := listener.(ShouldHandle); ok {
		if !handler.ShouldHandle(event) {
			return nil
		}
	}

	// Handle the event
	return listener.Handle(ctx, event)
}
