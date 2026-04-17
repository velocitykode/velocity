package events

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// DefaultDispatcher is the default event dispatcher implementation
type DefaultDispatcher struct {
	mu           sync.RWMutex
	listeners    map[string][]listenerEntry
	wildcards    map[string][]listenerEntry
	queue        QueueDispatcher // Optional queue dispatcher for async events
	nextID       int             // Counter for generating listener IDs
	listenerByID map[int]string  // Maps listener ID to event name for removal
}

// listenerEntry wraps a Listener with an ID for tracking
type listenerEntry struct {
	id       int
	listener Listener
}

// QueueDispatcher handles queued event dispatching
type QueueDispatcher interface {
	Push(event interface{}, listener Listener, delay time.Duration) error
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
func (d *DefaultDispatcher) Dispatch(event interface{}) error {
	if event == nil {
		return fmt.Errorf("events: cannot dispatch nil event")
	}
	return d.dispatchToListeners(event, func(listener Listener) error {
		if listener.ShouldQueue() && d.queue != nil {
			if err := d.queue.Push(event, listener, 0); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
			return nil
		}
		return d.processListener(event, listener)
	})
}

// DispatchNow fires an event synchronously to all listeners.
func (d *DefaultDispatcher) DispatchNow(event interface{}) error {
	return d.dispatchToListeners(event, func(listener Listener) error {
		return d.processListener(event, listener)
	})
}

// DispatchAsync fires an event asynchronously via the queue.
// Falls back to a goroutine if no queue is configured. Panics in the
// fallback goroutine are recovered so one bad listener does not tear
// down the process.
func (d *DefaultDispatcher) DispatchAsync(event interface{}) error {
	if d.queue == nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("velocity/events: dispatch async panic recovered: %v", panicerr.FromRecovered(r))
				}
			}()
			_ = d.DispatchNow(event)
		}()
		return nil
	}

	return d.dispatchToListeners(event, func(listener Listener) error {
		if err := d.queue.Push(event, listener, 0); err != nil {
			return fmt.Errorf("failed to queue listener: %w", err)
		}
		return nil
	})
}

// DispatchAfter fires an event after a delay.
// Falls back to a timer if no queue is configured.
func (d *DefaultDispatcher) DispatchAfter(event interface{}, delay time.Duration) error {
	if d.queue == nil {
		time.AfterFunc(delay, func() {
			_ = d.Dispatch(event)
		})
		return nil
	}

	return d.dispatchToListeners(event, func(listener Listener) error {
		if err := d.queue.Push(event, listener, delay); err != nil {
			return fmt.Errorf("failed to queue delayed listener: %w", err)
		}
		return nil
	})
}

// Until dispatches events until the first non-nil return
func (d *DefaultDispatcher) Until(event interface{}) (interface{}, error) {
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		// For Until, we need a special listener type that returns a value
		if handler, ok := listener.(interface {
			HandleWithResult(event interface{}) (interface{}, error)
		}); ok {
			if result, err := handler.HandleWithResult(event); err != nil || result != nil {
				return result, err
			}
		} else {
			// Regular listener, just check for error
			if err := listener.Handle(event); err != nil {
				return nil, err
			}
		}
	}

	return nil, nil
}

// Flush removes all listeners for an event
func (d *DefaultDispatcher) Flush(event string) {
	d.mu.Lock()
	defer d.mu.Unlock()

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

// GetListeners returns all listeners for an event
func (d *DefaultDispatcher) GetListeners(event interface{}) []Listener {
	return d.getListenersForEvent(event)
}

// getListenersForEvent retrieves all listeners for an event.
// Pre-allocates the result slice to avoid repeated grow-and-copy in hot paths.
func (d *DefaultDispatcher) getListenersForEvent(event interface{}) []Listener {
	d.mu.RLock()
	defer d.mu.RUnlock()

	eventName := d.getEventName(event)

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
func (d *DefaultDispatcher) dispatchToListeners(event interface{}, fn func(Listener) error) error {
	for _, listener := range d.getListenersForEvent(event) {
		if err := fn(listener); err != nil {
			return err
		}
	}
	return nil
}

// processListener executes a listener, recovering from panics.
func (d *DefaultDispatcher) processListener(event interface{}, listener Listener) (err error) {
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
	return listener.Handle(event)
}
