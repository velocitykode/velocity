package events

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// BatchingDispatcher batches events and dispatches them in groups
type BatchingDispatcher struct {
	*DefaultDispatcher
	mu            sync.RWMutex
	batchSize     int
	flushInterval time.Duration
	batch         []interface{}
	batchMu       sync.Mutex
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewBatchingDispatcher creates a new batching dispatcher
func NewBatchingDispatcher(batchSize int, flushInterval time.Duration) *BatchingDispatcher {
	d := &BatchingDispatcher{
		DefaultDispatcher: NewDispatcher(),
		batchSize:         batchSize,
		flushInterval:     flushInterval,
		batch:             make([]interface{}, 0, batchSize),
		stopCh:            make(chan struct{}),
	}

	// Start the flush ticker
	d.wg.Add(1)
	go d.flushLoop()

	return d
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

// Dispatch adds an event to the batch
func (d *BatchingDispatcher) Dispatch(event interface{}) error {
	d.batchMu.Lock()
	d.batch = append(d.batch, event)
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

	events := make([]interface{}, len(d.batch))
	copy(events, d.batch)
	d.batch = d.batch[:0]
	d.batchMu.Unlock()

	// Dispatch all events
	for _, event := range events {
		if err := d.DefaultDispatcher.Dispatch(event); err != nil {
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
	mu       sync.RWMutex
	debounce time.Duration
	timers   map[string]*time.Timer
	timersMu sync.RWMutex
	stopCh   chan struct{}
	wg       sync.WaitGroup
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

// Dispatch debounces event dispatching
func (d *DebouncingDispatcher) Dispatch(event interface{}) error {
	eventName := d.getEventName(event)

	d.timersMu.Lock()
	defer d.timersMu.Unlock()

	// Cancel existing timer if any
	if timer, exists := d.timers[eventName]; exists {
		timer.Stop()
	}

	// Create new timer
	d.timers[eventName] = time.AfterFunc(d.debounce, func() {
		d.DefaultDispatcher.Dispatch(event)
		d.timersMu.Lock()
		delete(d.timers, eventName)
		d.timersMu.Unlock()
	})

	return nil
}

// DispatchNow immediately dispatches an event, bypassing debounce
func (d *DebouncingDispatcher) DispatchNow(event interface{}) error {
	eventName := d.getEventName(event)

	d.timersMu.Lock()
	if timer, exists := d.timers[eventName]; exists {
		timer.Stop()
		delete(d.timers, eventName)
	}
	d.timersMu.Unlock()

	return d.DefaultDispatcher.Dispatch(event)
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
	mu           sync.RWMutex
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
func (d *ThrottlingDispatcher) Dispatch(event interface{}) error {
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

	return d.DefaultDispatcher.Dispatch(event)
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
	mu         sync.RWMutex
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
func (d *RateLimitedDispatcher) Dispatch(event interface{}) error {
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

	return d.DefaultDispatcher.Dispatch(event)
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
	mu        sync.RWMutex
	coalesce  time.Duration
	pending   map[string]*coalescedEvent
	pendingMu sync.RWMutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

type coalescedEvent struct {
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

// Dispatch coalesces events before dispatching
func (d *CoalescingDispatcher) Dispatch(event interface{}) error {
	eventName := d.getEventName(event)

	d.pendingMu.Lock()
	defer d.pendingMu.Unlock()

	// Check if event is already pending
	if ce, exists := d.pending[eventName]; exists {
		ce.timer.Stop()
		ce.event = event // Update to latest
		ce.count++
		ce.timer = time.AfterFunc(d.coalesce, func() {
			d.dispatchCoalesced(eventName)
		})
		return nil
	}

	// Create new pending event
	ce := &coalescedEvent{
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

	d.DefaultDispatcher.Dispatch(ce.event)
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
