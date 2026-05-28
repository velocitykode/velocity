package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
)

// batchEntry pairs a buffered event with the ctx that originally dispatched
// it so the eventual fan-out preserves request-scoped values.
type batchEntry struct {
	ctx   context.Context
	event interface{}
}

// BatchingDispatcher batches events and dispatches them in groups
type BatchingDispatcher struct {
	*DefaultDispatcher
	batchSize     int
	flushInterval time.Duration
	batch         []batchEntry
	batchMu       sync.Mutex
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewBatchingDispatcher creates a new batching dispatcher.
// Call Start() to begin the background flush goroutine.
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
	ticker := time.NewTicker(d.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			d.Flush()
		case <-d.stopCh:
			d.Flush()
			return
		}
	}
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

// Flush dispatches all batched events
func (d *BatchingDispatcher) Flush() error {
	d.batchMu.Lock()
	if len(d.batch) == 0 {
		d.batchMu.Unlock()
		return nil
	}

	entries := make([]batchEntry, len(d.batch))
	copy(entries, d.batch)
	d.batch = d.batch[:0]
	d.batchMu.Unlock()

	// Dispatch all events
	for _, entry := range entries {
		if err := d.DefaultDispatcher.Dispatch(entry.ctx, entry.event); err != nil {
			return err
		}
	}

	return nil
}

// Stop stops the batching dispatcher
func (d *BatchingDispatcher) Stop() {
	close(d.stopCh)
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
	timers   map[string]*time.Timer
	timersMu sync.RWMutex
	stopCh   chan struct{}
}

// NewDebouncingDispatcher creates a new debouncing dispatcher
func NewDebouncingDispatcher(debounce time.Duration) *DebouncingDispatcher {
	return &DebouncingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		debounce:          debounce,
		timers:            make(map[string]*time.Timer),
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
	if timer, exists := d.timers[eventName]; exists {
		timer.Stop()
	}

	// Create new timer
	d.timers[eventName] = time.AfterFunc(d.debounce, func() {
		d.DefaultDispatcher.Dispatch(bgCtx, event)
		d.timersMu.Lock()
		delete(d.timers, eventName)
		d.timersMu.Unlock()
	})

	return nil
}

// DispatchNow immediately dispatches an event, bypassing debounce
func (d *DebouncingDispatcher) DispatchNow(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	eventName := d.getEventName(event)

	d.timersMu.Lock()
	if timer, exists := d.timers[eventName]; exists {
		timer.Stop()
		delete(d.timers, eventName)
	}
	d.timersMu.Unlock()

	return d.DefaultDispatcher.Dispatch(ctx, event)
}

// Stop stops all debounce timers
func (d *DebouncingDispatcher) Stop() {
	d.timersMu.Lock()
	defer d.timersMu.Unlock()

	for _, timer := range d.timers {
		timer.Stop()
	}
	d.timers = make(map[string]*time.Timer)
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

	d.countMu.Lock()
	defer d.countMu.Unlock()

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
		return ErrRateLimitExceeded
	}

	// Add current timestamp
	validTimestamps = append(validTimestamps, now)
	d.eventCount[eventName] = validTimestamps

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
	wg        sync.WaitGroup
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

	d.DefaultDispatcher.Dispatch(ce.ctx, ce.event)
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

// Stop stops the coalescing dispatcher
func (d *CoalescingDispatcher) Stop() {
	d.cancel()
	d.pendingMu.Lock()
	for _, ce := range d.pending {
		ce.timer.Stop()
	}
	d.pending = make(map[string]*coalescedEvent)
	d.pendingMu.Unlock()
	d.wg.Wait()
}

// ErrRateLimitExceeded is returned when rate limit is exceeded
var ErrRateLimitExceeded = fmt.Errorf("event rate limit exceeded")
