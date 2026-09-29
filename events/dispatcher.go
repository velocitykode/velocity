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
	"github.com/velocitykode/velocity/internal/eventemit"
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
	mu           sync.RWMutex
	listeners    map[string][]listenerEntry
	wildcards    map[string][]listenerEntry
	queue        QueueDispatcher // Optional queue dispatcher for async events
	nextID       int             // Counter for generating listener IDs
	listenerByID map[int]string  // Maps listener ID to event name for removal

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
	// Each entry is tagged with the cacheEpoch under which it was built.
	// Every listener/wildcard mutation (Listen, Off, Flush) bumps cacheEpoch
	// under d.mu.Lock BEFORE it mutates listener state, so a concurrent
	// cache-hit dispatch (which bypasses d.mu) observing the new epoch finds
	// its entry stale and falls back to the locked resolve path -- where it
	// blocks behind the writer and sees the completed mutation. This restores
	// the pre-cache property that a dispatch starting after a writer takes
	// d.mu.Lock cannot fire a removed listener or miss a newly added one.
	resolvedCache sync.Map // resolvedKey -> resolvedListeners
	cacheEpoch    atomic.Uint64

	// failureReporter, when set, receives every dispatched event that
	// implements contract.FailureEvent, synchronously, before listener
	// fan-out. The framework wires it to ErrorHandler.Report at
	// bootstrap so background failures (failed jobs, scheduled tasks,
	// async listeners) reach the Reporter chain reliably even though
	// listener delivery may be asynchronous or best-effort.
	failureReporter func(ctx context.Context, event interface{}, err error)

	// reportingMu guards reporting. reporting holds the IDs of goroutines
	// currently inside a failureReporter call; reportFailure consults it so
	// a reporter that synchronously re-dispatches a failure event cannot
	// recurse through the bridge EVEN IF it swaps in a fresh context
	// (context.Background()), which the ctx marker alone cannot catch.
	reportingMu sync.Mutex
	reporting   map[uint64]struct{}

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
		listeners:    make(map[string][]listenerEntry),
		wildcards:    make(map[string][]listenerEntry),
		listenerByID: make(map[int]string),
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

	gid := eventemit.GoroutineID()
	d.reportingMu.Lock()
	_, inReporter := d.reporting[gid]
	d.reportingMu.Unlock()
	if inReporter {
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

	d.reportingMu.Lock()
	if d.reporting == nil {
		d.reporting = make(map[uint64]struct{})
	}
	d.reporting[gid] = struct{}{}
	d.reportingMu.Unlock()
	defer func() {
		d.reportingMu.Lock()
		delete(d.reporting, gid)
		d.reportingMu.Unlock()
	}()

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
	d.mu.Lock()
	defer d.mu.Unlock()

	// Invalidate the resolved cache before touching listener state so a
	// concurrent fast-path dispatch cannot keep using a pre-mutation slice.
	d.cacheEpoch.Add(1)

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
		// Try to get event name from type
		eventName := d.getEventName(e)
		d.addListener(eventName, listener, id)
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

	// Track ID to event mapping for removal
	d.listenerByID[id] = event
}

// Off removes a listener by its ID.
// Returns true if the listener was found and removed, false otherwise.
func (d *DefaultDispatcher) Off(id int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	eventName, exists := d.listenerByID[id]
	if !exists {
		return d.removeTyped(id)
	}

	// Invalidate the resolved cache before mutating listener state.
	d.cacheEpoch.Add(1)

	// Remove from the appropriate map based on whether it's a wildcard
	var removed bool
	if strings.Contains(eventName, "*") {
		d.wildcards[eventName], removed = d.removeListenerByID(d.wildcards[eventName], id)
		if len(d.wildcards[eventName]) == 0 {
			delete(d.wildcards, eventName)
		}
	} else {
		d.listeners[eventName], removed = d.removeListenerByID(d.listeners[eventName], id)
		if len(d.listeners[eventName]) == 0 {
			delete(d.listeners, eventName)
		}
	}

	if removed {
		delete(d.listenerByID, id)
	}

	return removed
}

// removeTyped removes the EventType listener with the given ID. Caller must
// hold d.mu.Lock.
func (d *DefaultDispatcher) removeTyped(id int) bool {
	for i, entry := range d.typed {
		if entry.id == id {
			// Invalidate the resolved cache before mutating listener state.
			d.cacheEpoch.Add(1)
			d.typed = append(d.typed[:i], d.typed[i+1:]...)
			return true
		}
	}
	return false
}

// removeListenerByID removes a listener entry by ID from a slice
func (d *DefaultDispatcher) removeListenerByID(entries []listenerEntry, id int) ([]listenerEntry, bool) {
	for i, entry := range entries {
		if entry.id == id {
			return append(entries[:i], entries[i+1:]...), true
		}
	}
	return entries, false
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
	deliver := func(listener Listener) error {
		// After-commit gate runs FIRST: a listener that opts into
		// post-commit delivery should never reach the queue or the
		// inline branch while the transaction is still in flight.
		// EnqueueAfterCommit returns false when no queue is installed
		// or the queue has already drained, which collapses the gate
		// into the existing inline / queue branches below.
		if ac, ok := listener.(ShouldDispatchAfterCommit); ok && ac.ShouldDispatchAfterCommit() {
			// Capture the listener and event for replay at commit
			// time. The replay uses commit-time ctx (not the in-flight
			// tx ctx) so listeners see post-transaction values.
			//
			// At commit time we re-check Async and the live queue
			// handle: a listener that opts into BOTH after-commit AND
			// queueing must still take the queue branch when the
			// transaction lands. Without this gate an Async
			// listener that also implements ShouldDispatchAfterCommit
			// would run synchronously on the commit goroutine, blocking
			// the orm wrapper return and silently changing the listener's
			// declared async semantics.
			ev := event
			ln := listener
			if EnqueueAfterCommit(ctx, func(replayCtx context.Context) error {
				if ln.Async() {
					d.mu.RLock()
					replayQueue := d.queue
					d.mu.RUnlock()
					if replayQueue != nil {
						if err := replayQueue.Push(replayCtx, ev, ln, 0); err != nil {
							return fmt.Errorf("failed to queue listener: %w", err)
						}
						return nil
					}
					// Queue was unwired between dispatch and commit
					// (rare). Fall through to inline so the listener
					// still runs rather than silently disappearing.
				}
				return d.processListener(replayCtx, ev, ln)
			}) {
				return nil
			}
			// Fall through: no queue installed (no transaction) or
			// the queue already drained. The listener fires inline
			// just like a non-opt-in listener would.
		}
		if listener.Async() && q != nil {
			if err := q.Push(ctx, event, listener, 0); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
			return nil
		}
		return d.processListener(ctx, event, listener)
	}
	if detached {
		d.deliverDetached(ctx, event, deliver)
		return nil
	}
	return d.dispatchToListeners(event, deliver)
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
	_ = d.dispatch(d.reportFailure(ctx, event), event, true)
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
		return d.processListener(ctx, event, listener)
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
func (d *DefaultDispatcher) deliverDetached(ctx context.Context, event interface{}, deliver func(Listener) error) {
	err := d.dispatchToListeners(event, func(listener Listener) error {
		err := deliver(listener)
		if err != nil {
			d.dispatchListenerFailure(ctx, event, listener, err)
		}
		return err
	})
	if err == nil {
		return
	}
	if record := d.detachedFailures.Load(); record != nil {
		(*record)(ctx, err, event)
	}
}

// SetDetachedFailureRecorder installs fn as the recorder of detached
// deliveries that failed: the no-queue fallbacks of DispatchAsync and
// DispatchAfter, the later deliveries of a debouncing or coalescing
// dispatcher, and the delivery of an AsyncFailed. No caller receives such
// a delivery's result, so fn is called once per delivery a listener
// failed on (however many did), with the listeners' failures joined, on
// the goroutine that delivered it, after each failure was dispatched as
// its AsyncFailed and reported. The framework installs the app's failure
// policy here, which counts the delivery and hands it to the failure hook.
// nil removes it. Safe for concurrent use with dispatching.
func (d *DefaultDispatcher) SetDetachedFailureRecorder(fn func(ctx context.Context, err error, event any)) {
	if fn == nil {
		d.detachedFailures.Store(nil)
		return
	}
	d.detachedFailures.Store(&fn)
}

// dispatchListenerFailure dispatches the AsyncFailed for listener, which
// failed with err during a detached delivery of event under ctx. The
// failure is reported through the failure-report bridge here, once, under
// ctx, which carries the caller's trace IDs; then the AsyncFailed is
// delivered to its own listeners detached, so a listener of AsyncFailed
// that fails in turn is reported as well. A failure while delivering an
// AsyncFailed is reported but not dispatched again, so a listener that
// fails on every event cannot loop.
func (d *DefaultDispatcher) dispatchListenerFailure(ctx context.Context, event interface{}, listener Listener, err error) {
	failed := newAsyncFailed(ctx, event, listener, err)
	ctx = d.reportFailure(ctx, failed)
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
			return fmt.Errorf("failed to queue listener: %w", err)
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
			return fmt.Errorf("failed to queue delayed listener: %w", err)
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
	d.cacheEpoch.Add(1)

	// Remove listener ID mappings for this event
	if entries, ok := d.listeners[event]; ok {
		for _, entry := range entries {
			delete(d.listenerByID, entry.id)
		}
	}
	delete(d.listeners, event)

	// Also remove matching wildcards
	for pattern, entries := range d.wildcards {
		if matchesPattern(event, pattern) {
			for _, entry := range entries {
				delete(d.listenerByID, entry.id)
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
	eventName := d.getEventName(event)
	cacheKey := resolvedKey{name: eventName, typ: reflect.TypeOf(event)}

	epoch := d.cacheEpoch.Load()

	// Fast path: no d.mu, no map scan, no alloc. The epoch tag rejects any
	// entry built before an in-progress or completed mutation, so a hit can
	// never return a pre-mutation slice once a writer has bumped the epoch.
	if cached, ok := d.resolvedCache.Load(cacheKey); ok {
		if entry := cached.(resolvedListeners); entry.epoch == epoch {
			return entry.listeners
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
			return entry.listeners
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
		if entry.key.matches(event) {
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
		if entry.key.matches(event) {
			result = append(result, entry.listener)
		}
	}

	d.resolvedCache.Store(cacheKey, resolvedListeners{epoch: epoch, listeners: result})
	return result
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
// listener result.
func (d *DefaultDispatcher) dispatchToListeners(event interface{}, fn func(Listener) error) error {
	var errs []error
	for _, listener := range d.getListenersForEvent(event) {
		if err := fn(listener); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// processListener executes a listener, recovering from panics.
func (d *DefaultDispatcher) processListener(ctx context.Context, event interface{}, listener Listener) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()

	// Check if listener should handle this event
	if handler, ok := listener.(ShouldHandle); ok {
		if !handler.ShouldHandle(event) {
			return nil
		}
	}

	// Handle the event
	return listener.Handle(ctx, event)
}
