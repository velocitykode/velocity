package events

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/internal/goroutine"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// batchEntry pairs a buffered event with the ctx that originally dispatched
// it so the eventual fan-out preserves request-scoped values.
type batchEntry struct {
	ctx   context.Context
	event interface{}
}

// BatchingDispatcher batches events and dispatches them in groups.
//
// A batch is flushed by the caller (Flush, or a Dispatch that fills the
// batch) or by the background loop Start runs (on each interval and once
// more on Stop). A caller's flush returns the first failure and keeps the
// failed entry and the rest for a retry. The background loop has no caller
// to return a failure to, so it delivers each entry detached, like a
// debounced or coalesced delivery: a listener's failure is dispatched as
// its AsyncFailed and recorded (see SetDetachedFailureRecorder), and the
// entry is not requeued. Before, a background flush dropped the failure
// and requeued the entry, retrying it on every interval without end.
type BatchingDispatcher struct {
	*DefaultDispatcher
	batchSize     int
	flushInterval time.Duration
	batch         []batchEntry
	batchMu       sync.Mutex
	flushing      bool
	stopCh        chan struct{}
	stopOnce      sync.Once
	wg            sync.WaitGroup
	// loopID is the background loop's goroutine id, 0 until it runs, so
	// a Stop called on that goroutine does not wait for itself.
	loopID atomic.Uint64
}

// NewBatchingDispatcher creates a new batching dispatcher.
//
// The background flush goroutine is NOT started automatically: the caller
// must invoke Start() explicitly after construction (explicit-Start
// convention). Until Start is called, events accumulate only up to batchSize
// and flush synchronously when the batch fills; the interval-based flush does
// not run.
func NewBatchingDispatcher(batchSize int, flushInterval time.Duration) *BatchingDispatcher {
	return &BatchingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		batchSize:         batchSize,
		flushInterval:     flushInterval,
		batch:             make([]batchEntry, 0, batchSize),
		stopCh:            make(chan struct{}),
	}
}

// Start begins the background goroutine that periodically flushes
// batched events. Must be called after construction.
func (d *BatchingDispatcher) Start() {
	d.wg.Add(1)
	async.Go(d.flushLoop)
}

// flushLoop periodically flushes the batch
func (d *BatchingDispatcher) flushLoop() {
	defer d.wg.Done()
	d.loopID.Store(goroutine.ID())
	ticker := time.NewTicker(d.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.flushDetached()
		case <-d.stopCh:
			d.flushDetached()
			return
		}
	}
}

// flushDetached is the background loop's flush: no caller receives its
// result, so each entry is delivered detached (see dispatchLater), and a
// failed entry is dispatched as its AsyncFailed and recorded, not
// requeued.
func (d *BatchingDispatcher) flushDetached() {
	entries := d.takeBatch()
	if entries == nil {
		return
	}
	defer d.endFlush()
	for _, entry := range entries {
		d.dispatchLater(entry.ctx, entry.event)
	}
}

// takeBatch takes the batch for a flush and marks the flush in progress.
// It returns nil when the batch is empty or a flush is already in
// progress: a re-entrant or concurrent flush is a no-op, so the one in
// progress keeps control of its entries and their order (see Flush).
func (d *BatchingDispatcher) takeBatch() []batchEntry {
	d.batchMu.Lock()
	if d.flushing || len(d.batch) == 0 {
		d.batchMu.Unlock()
		return nil
	}
	entries := make([]batchEntry, len(d.batch))
	copy(entries, d.batch)
	d.batch = d.batch[:0]
	d.flushing = true
	d.batchMu.Unlock()
	return entries
}

// endFlush marks the flush in progress as done.
func (d *BatchingDispatcher) endFlush() {
	d.batchMu.Lock()
	d.flushing = false
	d.batchMu.Unlock()
}

// Dispatch adds an event to the batch.
//
// Context semantics: the ctx is captured per-entry but stripped of
// cancellation and deadline via context.WithoutCancel before being stored,
// because the actual fan-out to listeners happens later (either when the
// batch fills or on the background flush goroutine after the request
// returns). Request-scoped values like trace IDs survive; the caller's
// cancellation does NOT propagate to listeners.
//
// Callers that need cancellation to propagate should not use the batching
// dispatcher; dispatch synchronously via DefaultDispatcher.Dispatch and let
// the listener choose its own backgrounding.
func (d *BatchingDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.batchMu.Lock()
	// Detach ctx from request lifetime: the actual fan-out may happen on the
	// background flush goroutine after the request returns, but values like
	// trace IDs should survive.
	d.batch = append(d.batch, batchEntry{ctx: context.WithoutCancel(ctx), event: event})
	shouldFlush := len(d.batch) >= d.batchSize
	d.batchMu.Unlock()

	if shouldFlush {
		return d.Flush()
	}

	return nil
}

// Flush dispatches all batched events.
//
// Partial-failure semantics: if a dispatch fails at entry i, the failing
// entry plus every entry after it are spliced back into the batch ahead of
// any events recorded re-entrantly during the flush, and the error is
// returned so a subsequent Flush resumes from the failed entry. Successful
// entries (0..i-1) are not redelivered. A panic while an entry is
// dispatched (its event's Name, say) is that entry's failure, returned as
// the recovered panic on the same path.
func (d *BatchingDispatcher) Flush() error {
	// A flush already in progress (re-entrant or concurrent call) must be a
	// no-op: the outer Flush retains exclusive control of its snapshot so it
	// can splice a failed entry plus the remainder ahead of any events
	// recorded re-entrantly by listeners during the flush. Letting a nested
	// Flush drain the batch here would deliver those re-entrant events before
	// the outer snapshot's remainder, violating ordering.
	entries := d.takeBatch()
	if entries == nil {
		return nil
	}

	if i, err := d.dispatchEntries(entries); err != nil {
		// Put the failing entry plus the remainder back ahead of any
		// events recorded re-entrantly during this flush so a retry
		// resumes from the failure without losing later entries.
		remainder := entries[i:]
		d.batchMu.Lock()
		if len(d.batch) > 0 {
			combined := make([]batchEntry, 0, len(remainder)+len(d.batch))
			combined = append(combined, remainder...)
			combined = append(combined, d.batch...)
			d.batch = combined
		} else {
			// Copy to detach from the snapshot's backing array.
			dup := make([]batchEntry, len(remainder))
			copy(dup, remainder)
			d.batch = dup
		}
		d.flushing = false
		d.batchMu.Unlock()
		return err
	}

	d.endFlush()
	return nil
}

// dispatchEntries dispatches entries in order until one fails, returning
// its index and failure. A panic while an entry is dispatched is that
// entry's failure, returned as the recovered panic; one recover covers the
// whole run, so an entry costs no more than its dispatch.
func (d *BatchingDispatcher) dispatchEntries(entries []batchEntry) (i int, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = panicerr.FromRecovered(p)
		}
	}()
	for i = range entries {
		if err = d.DefaultDispatcher.Dispatch(entries[i].ctx, entries[i].event); err != nil {
			return i, err
		}
	}
	return len(entries), nil
}

// Stop stops the batching dispatcher and waits for the background loop to
// finish its final flush. Safe to call more than once; the stopOnce guard
// prevents a panic on double close of stopCh. Called on the loop's own
// goroutine (by a listener its flush runs), Stop signals the loop and
// returns without waiting, since the loop cannot end while Stop runs on
// it; the loop ends after the flush in progress and its final flush.
func (d *BatchingDispatcher) Stop() {
	d.stopOnce.Do(func() {
		close(d.stopCh)
	})
	if id := d.loopID.Load(); id != 0 && id == goroutine.ID() {
		return
	}
	d.wg.Wait()
}

// GetBatchSize returns the current batch size
func (d *BatchingDispatcher) GetBatchSize() int {
	d.batchMu.Lock()
	defer d.batchMu.Unlock()
	return len(d.batch)
}

// DebouncingDispatcher debounces events to prevent rapid firing
type DebouncingDispatcher struct {
	*DefaultDispatcher
	debounce time.Duration
	timers   map[string]debounceTimer
	timersMu sync.RWMutex
	// timerGen numbers the timers, so a fired timer can tell whether the
	// entry for its name is still its own.
	timerGen uint64
	stopCh   chan struct{}
}

// debounceTimer is a pending debounced delivery: its timer and its number.
type debounceTimer struct {
	gen   uint64
	timer *time.Timer
}

// NewDebouncingDispatcher creates a new debouncing dispatcher
func NewDebouncingDispatcher(debounce time.Duration) *DebouncingDispatcher {
	return &DebouncingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		debounce:          debounce,
		timers:            make(map[string]debounceTimer),
		stopCh:            make(chan struct{}),
	}
}

// Dispatch debounces event dispatching. Rapid calls with the same event name
// reset a timer, and the actual fan-out happens on a background goroutine
// after the debounce window elapses without further activity.
//
// Context semantics: the ctx is captured but stripped of cancellation and
// deadline via context.WithoutCancel before being held by the debounce
// timer, because the underlying dispatch fires on a background goroutine
// that may run long after the caller has returned. Request-scoped values
// like trace IDs survive; the caller's cancellation does NOT propagate to
// listeners. A canceled parent ctx will not stop the pending dispatch.
//
// Callers who need cancellation to propagate should not use the debouncing
// dispatcher; dispatch synchronously via DefaultDispatcher.Dispatch and let
// the listener choose its own backgrounding.
func (d *DebouncingDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	bgCtx := context.WithoutCancel(ctx)
	eventName := d.getEventName(event)

	d.timersMu.Lock()
	defer d.timersMu.Unlock()

	// Cancel existing timer if any
	if pending, exists := d.timers[eventName]; exists {
		pending.timer.Stop()
	}

	// Create new timer. Once it has fired, it removes its own entry only:
	// a listener, or a concurrent Dispatch, may have installed a newer
	// timer for the name, which stays pending (and stoppable).
	d.timerGen++
	gen := d.timerGen
	d.timers[eventName] = debounceTimer{gen: gen, timer: time.AfterFunc(d.debounce, func() {
		d.dispatchLater(bgCtx, event)
		d.timersMu.Lock()
		if d.timers[eventName].gen == gen {
			delete(d.timers, eventName)
		}
		d.timersMu.Unlock()
	})}

	return nil
}

// DispatchNow immediately dispatches an event, bypassing debounce
func (d *DebouncingDispatcher) DispatchNow(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eventName := d.getEventName(event)

	d.timersMu.Lock()
	if pending, exists := d.timers[eventName]; exists {
		pending.timer.Stop()
		delete(d.timers, eventName)
	}
	d.timersMu.Unlock()

	return d.DefaultDispatcher.Dispatch(ctx, event)
}

// Stop stops all debounce timers
func (d *DebouncingDispatcher) Stop() {
	d.timersMu.Lock()
	defer d.timersMu.Unlock()

	for _, pending := range d.timers {
		pending.timer.Stop()
	}
	d.timers = make(map[string]debounceTimer)
}

// GetPendingCount returns the number of pending debounced events
func (d *DebouncingDispatcher) GetPendingCount() int {
	d.timersMu.RLock()
	defer d.timersMu.RUnlock()
	return len(d.timers)
}

// ThrottlingDispatcher throttles event dispatching to a maximum rate
type ThrottlingDispatcher struct {
	*DefaultDispatcher
	interval     time.Duration
	lastDispatch map[string]time.Time
	dispatchMu   sync.RWMutex
}

// NewThrottlingDispatcher creates a new throttling dispatcher
func NewThrottlingDispatcher(interval time.Duration) *ThrottlingDispatcher {
	return &ThrottlingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		interval:          interval,
		lastDispatch:      make(map[string]time.Time),
	}
}

// Dispatch throttles event dispatching
func (d *ThrottlingDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eventName := d.getEventName(event)

	d.dispatchMu.Lock()
	lastTime, exists := d.lastDispatch[eventName]
	now := time.Now()

	if exists && now.Sub(lastTime) < d.interval {
		d.dispatchMu.Unlock()
		return nil // Skip dispatching
	}

	d.lastDispatch[eventName] = now
	d.dispatchMu.Unlock()

	return d.DefaultDispatcher.Dispatch(ctx, event)
}

// CanDispatch checks if an event can be dispatched now
func (d *ThrottlingDispatcher) CanDispatch(event interface{}) bool {
	eventName := d.getEventName(event)

	d.dispatchMu.RLock()
	defer d.dispatchMu.RUnlock()

	lastTime, exists := d.lastDispatch[eventName]
	if !exists {
		return true
	}

	return time.Since(lastTime) >= d.interval
}

// Reset resets the throttle state for an event
func (d *ThrottlingDispatcher) Reset(eventName string) {
	d.dispatchMu.Lock()
	defer d.dispatchMu.Unlock()
	delete(d.lastDispatch, eventName)
}

// RateLimitedDispatcher provides rate limiting for event dispatching
type RateLimitedDispatcher struct {
	*DefaultDispatcher
	maxEvents  int
	window     time.Duration
	eventCount map[string][]time.Time
	countMu    sync.RWMutex
}

// NewRateLimitedDispatcher creates a new rate-limited dispatcher
func NewRateLimitedDispatcher(maxEvents int, window time.Duration) *RateLimitedDispatcher {
	return &RateLimitedDispatcher{
		DefaultDispatcher: NewDispatcher(),
		maxEvents:         maxEvents,
		window:            window,
		eventCount:        make(map[string][]time.Time),
	}
}

// Dispatch dispatches events with rate limiting
func (d *RateLimitedDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eventName := d.getEventName(event)

	// Compute the admit/deny decision and record the timestamp under the
	// lock, then release it BEFORE fanning out. Holding countMu across
	// DefaultDispatcher.Dispatch would deadlock a listener that re-enters
	// the same dispatcher.
	d.countMu.Lock()

	now := time.Now()
	cutoff := now.Add(-d.window)

	// Get existing timestamps
	timestamps := d.eventCount[eventName]

	// Remove old timestamps
	var validTimestamps []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			validTimestamps = append(validTimestamps, ts)
		}
	}

	// Check rate limit
	if len(validTimestamps) >= d.maxEvents {
		d.countMu.Unlock()
		return ErrRateLimitExceeded
	}

	// Add current timestamp
	validTimestamps = append(validTimestamps, now)
	d.eventCount[eventName] = validTimestamps
	d.countMu.Unlock()

	return d.DefaultDispatcher.Dispatch(ctx, event)
}

// GetRemainingEvents returns the number of events that can still be dispatched
func (d *RateLimitedDispatcher) GetRemainingEvents(eventName string) int {
	d.countMu.RLock()
	defer d.countMu.RUnlock()

	timestamps := d.eventCount[eventName]
	now := time.Now()
	cutoff := now.Add(-d.window)

	count := 0
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			count++
		}
	}

	remaining := d.maxEvents - count
	if remaining < 0 {
		remaining = 0
	}

	return remaining
}

// CoalescingDispatcher coalesces rapid identical events into a single dispatch
type CoalescingDispatcher struct {
	*DefaultDispatcher
	coalesce  time.Duration
	pending   map[string]*coalescedEvent
	pendingMu sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
}

type coalescedEvent struct {
	ctx   context.Context
	event interface{}
	timer *time.Timer
	count int
}

// NewCoalescingDispatcher creates a new coalescing dispatcher
func NewCoalescingDispatcher(coalesce time.Duration) *CoalescingDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &CoalescingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		coalesce:          coalesce,
		pending:           make(map[string]*coalescedEvent),
		ctx:               ctx,
		cancel:            cancel,
	}
}

// Dispatch coalesces events before dispatching. Rapid calls with the same
// event name collapse into a single eventual dispatch using the most recent
// event payload and ctx, fired on a background goroutine after the coalesce
// window elapses.
//
// Context semantics: the ctx is captured per pending event but stripped of
// cancellation and deadline via context.WithoutCancel before being stored,
// because the actual dispatch fires on a background goroutine that may run
// long after the caller has returned. Request-scoped values like trace IDs
// survive; the caller's cancellation does NOT propagate to listeners. A
// canceled parent ctx will not stop the pending dispatch, and because
// later calls overwrite the stored ctx, listeners only ever see the values
// from the most recent caller.
//
// Callers who need cancellation to propagate should not use the coalescing
// dispatcher; dispatch synchronously via DefaultDispatcher.Dispatch and let
// the listener choose its own backgrounding.
func (d *CoalescingDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	bgCtx := context.WithoutCancel(ctx)
	eventName := d.getEventName(event)

	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()

	// Check if event is already pending
	if ce, exists := d.pending[eventName]; exists {
		ce.timer.Stop()
		ce.ctx = bgCtx
		ce.event = event // Update to latest
		ce.count++
		ce.timer = time.AfterFunc(d.coalesce, func() {
			d.dispatchCoalesced(eventName)
		})
		return nil
	}

	// Create new pending event
	ce := &coalescedEvent{
		ctx:   bgCtx,
		event: event,
		count: 1,
	}
	ce.timer = time.AfterFunc(d.coalesce, func() {
		d.dispatchCoalesced(eventName)
	})
	d.pending[eventName] = ce

	return nil
}

// dispatchCoalesced dispatches a coalesced event
func (d *CoalescingDispatcher) dispatchCoalesced(eventName string) {
	d.pendingMu.Lock()
	ce, exists := d.pending[eventName]
	if !exists {
		d.pendingMu.Unlock()
		return
	}
	delete(d.pending, eventName)
	d.pendingMu.Unlock()

	d.dispatchLater(ce.ctx, ce.event)
}

// GetCoalescedCount returns how many times an event has been coalesced
func (d *CoalescingDispatcher) GetCoalescedCount(eventName string) int {
	d.pendingMu.RLock()
	defer d.pendingMu.RUnlock()

	if ce, exists := d.pending[eventName]; exists {
		return ce.count
	}
	return 0
}

// Stop stops the coalescing dispatcher by cancelling its context and
// stopping every pending coalesce timer.
//
// Stop does NOT wait for an in-flight dispatchCoalesced to finish: a timer
// that has already fired runs its AfterFunc goroutine to completion
// independently, and that goroutine is not tracked. Stop only guarantees
// that timers which have not yet fired will not fire.
func (d *CoalescingDispatcher) Stop() {
	d.cancel()
	d.pendingMu.Lock()
	for _, ce := range d.pending {
		ce.timer.Stop()
	}
	d.pending = make(map[string]*coalescedEvent)
	d.pendingMu.Unlock()
}

// ErrRateLimitExceeded is returned when rate limit is exceeded
var ErrRateLimitExceeded = fmt.Errorf("event rate limit exceeded")
