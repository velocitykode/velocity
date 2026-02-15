package events

import (
	"fmt"
	"sync"
	"time"
)

// FakeDispatcher is a fake event dispatcher for testing
type FakeDispatcher struct {
	mu           sync.RWMutex
	events       []interface{}
	listeners    map[string][]listenerEntry
	listenerByID map[int]string
	nextID       int
	shouldFake   bool
}

// NewFakeDispatcher creates a new fake dispatcher
func NewFakeDispatcher() *FakeDispatcher {
	return &FakeDispatcher{
		events:       make([]interface{}, 0),
		listeners:    make(map[string][]listenerEntry),
		listenerByID: make(map[int]string),
		shouldFake:   true,
	}
}

// Listen registers a listener (but won't execute in fake mode).
// Returns a listener ID that can be used with Off() to unregister.
func (f *FakeDispatcher) Listen(events interface{}, listener Listener) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.nextID++
	id := f.nextID

	var eventNames []string
	switch e := events.(type) {
	case string:
		eventNames = []string{e}
	case []string:
		eventNames = e
	default:
		eventNames = []string{f.getEventName(e)}
	}

	for _, event := range eventNames {
		entry := listenerEntry{id: id, listener: listener}
		f.listeners[event] = append(f.listeners[event], entry)
		f.listenerByID[id] = event
	}

	return id
}

// Off removes a listener by its ID.
// Returns true if the listener was found and removed, false otherwise.
func (f *FakeDispatcher) Off(id int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	eventName, exists := f.listenerByID[id]
	if !exists {
		return false
	}

	entries := f.listeners[eventName]
	for i, entry := range entries {
		if entry.id == id {
			f.listeners[eventName] = append(entries[:i], entries[i+1:]...)
			if len(f.listeners[eventName]) == 0 {
				delete(f.listeners, eventName)
			}
			delete(f.listenerByID, id)
			return true
		}
	}

	return false
}

// Subscribe registers an event subscriber
func (f *FakeDispatcher) Subscribe(subscriber Subscriber) {
	subscriber.Subscribe(f)
}

// Dispatch records the event without executing listeners
func (f *FakeDispatcher) Dispatch(event interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.shouldFake {
		f.events = append(f.events, event)
		return nil
	}

	// If not faking, execute listeners
	return f.executeListeners(event)
}

// DispatchNow records the event synchronously
func (f *FakeDispatcher) DispatchNow(event interface{}) error {
	return f.Dispatch(event)
}

// DispatchAsync records the event asynchronously
func (f *FakeDispatcher) DispatchAsync(event interface{}) error {
	return f.Dispatch(event)
}

// DispatchAfter records the event with delay
func (f *FakeDispatcher) DispatchAfter(event interface{}, delay time.Duration) error {
	return f.Dispatch(event)
}

// Until dispatches events until the first non-nil return
func (f *FakeDispatcher) Until(event interface{}) (interface{}, error) {
	f.Dispatch(event)
	return nil, nil
}

// Flush removes all listeners for an event
func (f *FakeDispatcher) Flush(event string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Remove listener ID mappings
	if entries, ok := f.listeners[event]; ok {
		for _, entry := range entries {
			delete(f.listenerByID, entry.id)
		}
	}
	delete(f.listeners, event)
}

// Forget removes specific listeners
func (f *FakeDispatcher) Forget(event string) {
	f.Flush(event)
}

// HasListeners checks if an event has listeners
func (f *FakeDispatcher) HasListeners(event interface{}) bool {
	f.mu.RLock()
	defer f.mu.RUnlock()

	eventName := f.getEventName(event)
	_, ok := f.listeners[eventName]
	return ok
}

// GetListeners returns all listeners for an event
func (f *FakeDispatcher) GetListeners(event interface{}) []Listener {
	f.mu.RLock()
	defer f.mu.RUnlock()

	eventName := f.getEventName(event)
	entries := f.listeners[eventName]
	listeners := make([]Listener, len(entries))
	for i, entry := range entries {
		listeners[i] = entry.listener
	}
	return listeners
}

// countMatchingEvents returns the number of dispatched events matching the given type.
// Caller must hold at least an RLock on f.mu.
func (f *FakeDispatcher) countMatchingEvents(eventType interface{}) (string, int) {
	eventTypeName := resolveTypeName(eventType)
	count := 0
	for _, event := range f.events {
		if resolveTypeName(event) == eventTypeName {
			count++
		}
	}
	return eventTypeName, count
}

// AssertDispatched asserts that an event was dispatched
func (f *FakeDispatcher) AssertDispatched(eventType interface{}, callback func(interface{}) bool) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	eventTypeName := resolveTypeName(eventType)
	for _, event := range f.events {
		if resolveTypeName(event) == eventTypeName {
			if callback == nil || callback(event) {
				return nil
			}
		}
	}

	return fmt.Errorf("event %s was not dispatched", eventTypeName)
}

// AssertDispatchedTimes asserts an event was dispatched n times
func (f *FakeDispatcher) AssertDispatchedTimes(eventType interface{}, times int) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	eventTypeName, count := f.countMatchingEvents(eventType)
	if count != times {
		return fmt.Errorf("event %s was dispatched %d times, expected %d", eventTypeName, count, times)
	}
	return nil
}

// AssertNotDispatched asserts that an event was not dispatched
func (f *FakeDispatcher) AssertNotDispatched(eventType interface{}) error {
	f.mu.RLock()
	defer f.mu.RUnlock()

	eventTypeName, count := f.countMatchingEvents(eventType)
	if count > 0 {
		return fmt.Errorf("event %s was dispatched but should not have been", eventTypeName)
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

// executeListeners executes listeners for an event (when not faking)
func (f *FakeDispatcher) executeListeners(event interface{}) error {
	eventName := f.getEventName(event)
	entries := f.listeners[eventName]

	for _, entry := range entries {
		if err := entry.listener.Handle(event); err != nil {
			return err
		}
	}

	return nil
}

// getEventName extracts the event name without camelToDot conversion.
// FakeDispatcher preserves raw type names for non-Event/non-string types
// (e.g., "NamedType" stays as "NamedType"), unlike DefaultDispatcher
// which converts to dot notation (e.g., "named.type").
func (f *FakeDispatcher) getEventName(event interface{}) string {
	return resolveEventNameRaw(event)
}
