package events

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// FakeDispatcher is a fake event dispatcher for testing. It records every
// dispatched event instead of running listeners (until StopFaking), and it
// resolves which listeners an event reaches, and which recorded events an
// assertion names, exactly as DefaultDispatcher does.
type FakeDispatcher struct {
	mu         sync.RWMutex
	events     []interface{}
	shouldFake bool

	// registry holds the listeners. It is a DefaultDispatcher so Listen,
	// Off, Flush, HasListeners, GetListeners and the listeners run after
	// StopFaking follow the real dispatcher's resolution: the same names,
	// patterns and keys. It has its own lock, never held while a listener
	// runs.
	registry *DefaultDispatcher
}

// NewFakeDispatcher creates a new fake dispatcher
func NewFakeDispatcher() *FakeDispatcher {
	return &FakeDispatcher{
		events:     make([]interface{}, 0),
		shouldFake: true,
		registry:   NewDispatcher(),
	}
}

// Listen registers a listener under key, as DefaultDispatcher.Listen does.
// While faking, listeners are recorded but not run. Returns a listener ID
// that can be used with Off() to unregister.
func (f *FakeDispatcher) Listen(key interface{}, listener Listener) int {
	return f.registry.Listen(key, listener)
}

// Off removes a listener by its ID.
// Returns true if the listener was found and removed, false otherwise.
func (f *FakeDispatcher) Off(id int) bool {
	return f.registry.Off(id)
}

// Subscribe registers an event subscriber
func (f *FakeDispatcher) Subscribe(subscriber Subscriber) {
	subscriber.Subscribe(f)
}

// Dispatch records the event without executing listeners.
//
// When StopFaking has been called, listeners run for real. Listener bodies
// commonly re-enter the dispatcher (a listener that dispatches a follow-up
// event, calls AssertDispatched on its own dispatcher, or otherwise touches
// any FakeDispatcher method) and any re-entrant call must be able to acquire
// the dispatcher's lock. We therefore use a two-phase pattern: record / snapshot
// listeners under the lock, release the lock, then invoke listeners outside
// the critical section. Holding f.mu across listener execution would deadlock
// the moment a listener body re-enters the FakeDispatcher.
func (f *FakeDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	f.mu.Lock()
	if f.shouldFake {
		f.events = append(f.events, event)
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	// Not faking: run listeners for real. executeListeners re-acquires the
	// lock as an RLock to snapshot the listener set, then releases it before
	// invoking listener bodies so a listener that re-enters the dispatcher
	// (AssertDispatched, Dispatch follow-ups, etc.) does not deadlock.
	return f.executeListeners(ctx, event)
}

// DispatchNow records the event synchronously
func (f *FakeDispatcher) DispatchNow(ctx context.Context, event interface{}) error {
	return f.Dispatch(ctx, event)
}

// DispatchAsync records the event asynchronously
func (f *FakeDispatcher) DispatchAsync(ctx context.Context, event interface{}) error {
	return f.Dispatch(ctx, event)
}

// DispatchAfter records the event with delay
func (f *FakeDispatcher) DispatchAfter(ctx context.Context, event interface{}, delay time.Duration) error {
	return f.Dispatch(ctx, event)
}

// Until dispatches events until the first non-nil return
func (f *FakeDispatcher) Until(ctx context.Context, event interface{}) (interface{}, error) {
	if err := f.Dispatch(ctx, event); err != nil {
		return nil, err
	}
	return nil, nil
}

// Flush removes all listeners for an event, as DefaultDispatcher.Flush does.
func (f *FakeDispatcher) Flush(event string) {
	f.registry.Flush(event)
}

// Forget removes specific listeners
func (f *FakeDispatcher) Forget(event string) {
	f.Flush(event)
}

// HasListeners checks if an event has listeners, as
// DefaultDispatcher.HasListeners does.
func (f *FakeDispatcher) HasListeners(event interface{}) bool {
	return f.registry.HasListeners(event)
}

// GetListeners returns all listeners for an event, as
// DefaultDispatcher.GetListeners does.
func (f *FakeDispatcher) GetListeners(event interface{}) []Listener {
	return f.registry.GetListeners(event)
}

// recordedMatcher returns how an assertion names key and the predicate that
// selects the recorded events it names. A string key selects the events a
// listener registered under that key would receive: the name, or the
// pattern, matched against each event's resolved name. Any other key
// selects events of its Go type, pointers dereferenced.
func recordedMatcher(key interface{}) (string, func(event interface{}) bool) {
	if pattern, ok := key.(string); ok {
		return pattern, func(event interface{}) bool {
			return matchesPattern(resolveEventName(event), pattern)
		}
	}
	typeName := resolveTypeName(key)
	return typeName, func(event interface{}) bool {
		return resolveTypeName(event) == typeName
	}
}

// countMatchingEvents returns how key is named and the number of recorded
// events it selects. Caller must hold at least an RLock on f.mu.
func (f *FakeDispatcher) countMatchingEvents(key interface{}) (string, int) {
	name, matches := recordedMatcher(key)
	count := 0
	for _, event := range f.events {
		if matches(event) {
			count++
		}
	}
	return name, count
}

// AssertDispatched asserts that an event key selects was dispatched and, when
// callback is non-nil, that callback accepts one of them. A string key
// selects events by name (or pattern) as a listener registered under it
// would; any other value selects events of its Go type.
func (f *FakeDispatcher) AssertDispatched(key interface{}, callback func(interface{}) bool) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	name, matches := recordedMatcher(key)
	for _, event := range f.events {
		if matches(event) {
			if callback == nil || callback(event) {
				return nil
			}
		}
	}

	return fmt.Errorf("event %s was not dispatched", name)
}

// AssertDispatchedTimes asserts that events key selects were dispatched
// exactly times times. Keys select as in AssertDispatched.
func (f *FakeDispatcher) AssertDispatchedTimes(key interface{}, times int) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	name, count := f.countMatchingEvents(key)
	if count != times {
		return fmt.Errorf("event %s was dispatched %d times, expected %d", name, count, times)
	}
	return nil
}

// AssertNotDispatched asserts that no event key selects was dispatched.
// Keys select as in AssertDispatched.
func (f *FakeDispatcher) AssertNotDispatched(key interface{}) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	name, count := f.countMatchingEvents(key)
	if count > 0 {
		return fmt.Errorf("event %s was dispatched but should not have been", name)
	}
	return nil
}

// AssertNothingDispatched asserts that no events were dispatched
func (f *FakeDispatcher) AssertNothingDispatched() error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	if len(f.events) > 0 {
		return fmt.Errorf("%d events were dispatched but none were expected", len(f.events))
	}

	return nil
}

// GetDispatchedEvents returns all dispatched events
func (f *FakeDispatcher) GetDispatchedEvents() []interface{} {
	f.mu.RLock()
	defer f.mu.RUnlock()

	events := make([]interface{}, len(f.events))
	copy(events, f.events)
	return events
}

// ClearEvents clears all recorded events
func (f *FakeDispatcher) ClearEvents() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = make([]interface{}, 0)
}

// StopFaking stops faking and executes listeners normally
func (f *FakeDispatcher) StopFaking() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shouldFake = false
}

// StartFaking starts faking events again
func (f *FakeDispatcher) StartFaking() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shouldFake = true
}

// executeListeners executes listeners for an event (when not faking).
//
// The registry resolves the listeners under its own lock and releases it
// before any listener body runs, so a listener that re-enters the
// dispatcher (AssertDispatched, a follow-up Dispatch, Listen) does not
// deadlock.
func (f *FakeDispatcher) executeListeners(ctx context.Context, event interface{}) error {
	for _, listener := range f.registry.getListenersForEvent(event) {
		if err := listener.Handle(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
