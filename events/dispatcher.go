package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
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

	// resolvedCache memoizes the fully-resolved []Listener slice (exact +
	// wildcard matches) keyed by event name, so the hot Dispatch path skips
	// the per-call slice allocation and double scan of d.wildcards. It is a
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
	resolvedCache sync.Map // event name (string) -> resolvedListeners
	cacheEpoch    atomic.Uint64

	// failureReporter, when set, receives every dispatched event that
	// implements contract.FailureEvent, synchronously, before listener
	// fan-out. The framework wires it to ExceptionHandler.Report at
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
}

// listenerEntry wraps a Listener with an ID for tracking
type listenerEntry struct {
	id       int
	listener Listener
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
// deterministically via the report flag on dispatch/dispatchNow.
type failureReportedKey struct{}

// SetFailureReporter installs the bridge that forwards FailureEvent
// dispatches to the exception Reporter chain. Pass nil to disable.
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
//     report flag instead and do not depend on this identity check.)
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

	gid := goroutineID()
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
// bridge deterministically via the report flag on dispatch/dispatchNow);
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

// gidParseFallback feeds goroutineID's failure path with unique sentinels.
// Sentinels live above 1<<63 so they can never collide with a real goroutine
// ID within the lifetime of a process.
var gidParseFallback atomic.Uint64

// goroutineID returns the running goroutine's ID by parsing the first line
// of runtime.Stack ("goroutine N [...]"). Used only on the failure-report
// path, which is rare by construction; the cost is acceptable there and the
// per-goroutine guard it enables cannot be built from context alone.
//
// The header format is not a formally stable runtime API (though it has been
// stable in practice for many releases and is relied on by widely used
// libraries), so the failure mode is chosen deliberately: if parsing ever
// fails, the function returns a process-unique sentinel instead of a shared
// zero value. A shared zero would make every unparsed goroutine look like
// the same goroutine and falsely suppress unrelated reports whenever any
// reporter is active; a unique sentinel merely degrades the recursion guard
// to a no-op for that one call (the ctx-marker guard still applies), which
// errs on the side of reporting rather than suppressing.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	const prefix = "goroutine "
	s := buf[:n]
	if len(s) <= len(prefix) {
		return 1<<63 | gidParseFallback.Add(1)
	}
	var id uint64
	digits := 0
	for _, c := range s[len(prefix):] {
		if c < '0' || c > '9' {
			break
		}
		id = id*10 + uint64(c-'0')
		digits++
	}
	if digits == 0 {
		return 1<<63 | gidParseFallback.Add(1)
	}
	return id
}

// Listen adds a listener for the given events. Multiple listeners may be registered
// for the same event (append semantics -- duplicates are intentional, not an error).
// Returns a listener ID that can be used with Off() to unregister the listener.
// Panics with *contract.RegistrationError if listener is nil.
func (d *DefaultDispatcher) Listen(events interface{}, listener Listener) int {
	if listener == nil {
		panic(contract.NewRegistrationError("events", "nil listener"))
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
	switch e := events.(type) {
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
		return false
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
// Listeners that return true from ShouldQueue are dispatched via the queue;
// all others are processed synchronously. Returns an error if event is nil.
//
// After-commit gating: a listener that implements
// ShouldDispatchAfterCommit and returns true is queued onto the
// after-commit task list attached to ctx (events.PrepareAfterCommit +
// orm.Manager.Transaction). The listener fires only if the surrounding
// transaction commits and is dropped on rollback. Outside a transaction
// (no queue on ctx) the listener fires inline so behaviour is unchanged
// for callers that have not wired the orm hook. Non-opt-in listeners
// always fire inline (or via the queue if ShouldQueue is true) regardless
// of the after-commit queue state.
func (d *DefaultDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	return d.dispatch(ctx, event, true)
}

// dispatch is the Dispatch core. report selects whether the failure-reporter
// bridge runs: every public entry point passes true; the bridge-internal
// re-dispatch paths (DispatchAfter's no-queue timer fallback) pass false so
// "exactly once" holds DETERMINISTICALLY for every event value, comparable or
// not, instead of depending on the ctx marker's identity comparison.
func (d *DefaultDispatcher) dispatch(ctx context.Context, event interface{}, report bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if event == nil {
		return fmt.Errorf("events: cannot dispatch nil event")
	}
	if report {
		ctx = d.reportFailure(ctx, event)
	}
	d.mu.RLock()
	q := d.queue
	d.mu.RUnlock()
	return d.dispatchToListeners(event, func(listener Listener) error {
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
			// At commit time we re-check ShouldQueue and the live queue
			// handle: a listener that opts into BOTH after-commit AND
			// queueing must still take the queue branch when the
			// transaction lands. Without this gate a ShouldQueue
			// listener that also implements ShouldDispatchAfterCommit
			// would run synchronously on the commit goroutine, blocking
			// the orm wrapper return and silently changing the listener's
			// declared async semantics.
			ev := event
			ln := listener
			if EnqueueAfterCommit(ctx, func(replayCtx context.Context) error {
				if ln.ShouldQueue() {
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
		if listener.ShouldQueue() && q != nil {
			if err := q.Push(ctx, event, listener, 0); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
			return nil
		}
		return d.processListener(ctx, event, listener)
	})
}

// DispatchNow fires an event synchronously to all listeners.
func (d *DefaultDispatcher) DispatchNow(ctx context.Context, event interface{}) error {
	return d.dispatchNow(ctx, event, true)
}

// dispatchNow is the DispatchNow core; see dispatch for the report flag.
// DispatchAsync's no-queue goroutine fallback passes false because the public
// DispatchAsync call already reported synchronously at the point of dispatch.
func (d *DefaultDispatcher) dispatchNow(ctx context.Context, event interface{}, report bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if report {
		ctx = d.reportFailure(ctx, event)
	}
	return d.dispatchToListeners(event, func(listener Listener) error {
		return d.processListener(ctx, event, listener)
	})
}

// DispatchAsync fires an event asynchronously via the queue.
// Falls back to a panic-safe goroutine (async.Go) if no queue is configured.
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
		// report=false: the failure was already reported above; skipping
		// the bridge here is deterministic and does not depend on the ctx
		// marker's identity comparison (uncomparable events included).
		bgCtx := context.WithoutCancel(ctx)
		async.Go(func() {
			_ = d.dispatchNow(bgCtx, event, false)
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
// Falls back to a timer if no queue is configured.
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
		// report=false: already reported above; the deterministic skip
		// replaces reliance on the ctx marker's identity comparison, which
		// cannot dedupe uncomparable event values.
		time.AfterFunc(delay, func() {
			_ = d.dispatch(bgCtx, event, false)
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
		if d.matchesPattern(event, pattern) {
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
// The fully-resolved slice is memoized in resolvedCache keyed by event name,
// so the common exact-match path returns the cached slice with no map scan and
// no per-dispatch allocation. On a miss it builds the slice once (exact +
// wildcard matches) under d.mu.RLock and stores it. Callers must treat the
// returned slice as read-only: it is shared with the cache. The only in-place
// mutator, PriorityDispatcher.getListenersForEvent, clones before sorting.
func (d *DefaultDispatcher) getListenersForEvent(event interface{}) []Listener {
	eventName := d.getEventName(event)

	epoch := d.cacheEpoch.Load()

	// Fast path: no d.mu, no map scan, no alloc. The epoch tag rejects any
	// entry built before an in-progress or completed mutation, so a hit can
	// never return a pre-mutation slice once a writer has bumped the epoch.
	if cached, ok := d.resolvedCache.Load(eventName); ok {
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
	if cached, ok := d.resolvedCache.Load(eventName); ok {
		if entry := cached.(resolvedListeners); entry.epoch == epoch {
			return entry.listeners
		}
	}

	// Pre-compute capacity to avoid repeated slice growth
	capacity := len(d.listeners[eventName])
	for pattern, entries := range d.wildcards {
		if d.matchesPattern(eventName, pattern) {
			capacity += len(entries)
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
		if d.matchesPattern(eventName, pattern) {
			for _, entry := range entries {
				result = append(result, entry.listener)
			}
		}
	}

	d.resolvedCache.Store(eventName, resolvedListeners{epoch: epoch, listeners: result})
	return result
}

// getEventName extracts the event name from various types.
func (d *DefaultDispatcher) getEventName(event interface{}) string {
	return resolveEventName(event)
}

// matchesPattern checks if an event matches a wildcard pattern
func (d *DefaultDispatcher) matchesPattern(event, pattern string) bool {
	// Simple wildcard matching
	if pattern == "*" {
		return true
	}

	// Handle patterns like "user.*"
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(event, prefix+".")
	}

	// Handle patterns like "*.created"
	if strings.HasPrefix(pattern, "*.") {
		suffix := strings.TrimPrefix(pattern, "*.")
		return strings.HasSuffix(event, "."+suffix)
	}

	// Handle patterns with * in the middle
	if strings.Contains(pattern, "*") {
		parts := strings.Split(pattern, "*")
		if len(parts) == 2 {
			return strings.HasPrefix(event, parts[0]) && strings.HasSuffix(event, parts[1])
		}
	}

	return event == pattern
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
