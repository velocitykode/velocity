package events

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// DefaultDispatcher is the default event dispatcher implementation
type DefaultDispatcher struct {
	mu        sync.RWMutex
	listeners map[string][]Listener
	wildcards map[string][]Listener
	queue     QueueDispatcher // Optional queue dispatcher for async events
}

// QueueDispatcher handles queued event dispatching
type QueueDispatcher interface {
	Push(event interface{}, listener Listener, delay time.Duration) error
}

// NewDispatcher creates a new event dispatcher
func NewDispatcher() *DefaultDispatcher {
	return &DefaultDispatcher{
		listeners: make(map[string][]Listener),
		wildcards: make(map[string][]Listener),
	}
}

// SetQueueDispatcher sets the queue dispatcher for async events
func (d *DefaultDispatcher) SetQueueDispatcher(qd QueueDispatcher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.queue = qd
}

// Listen registers a listener for one or more events
func (d *DefaultDispatcher) Listen(events interface{}, listener Listener) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Handle different event types
	switch e := events.(type) {
	case string:
		d.addListener(e, listener)
	case []string:
		for _, event := range e {
			d.addListener(event, listener)
		}
	default:
		// Try to get event name from type
		eventName := d.getEventName(e)
		d.addListener(eventName, listener)
	}
}

// addListener adds a listener to the appropriate map
func (d *DefaultDispatcher) addListener(event string, listener Listener) {
	// Check if it's a wildcard pattern
	if strings.Contains(event, "*") {
		d.wildcards[event] = append(d.wildcards[event], listener)
	} else {
		d.listeners[event] = append(d.listeners[event], listener)
	}
}

// Subscribe registers an event subscriber
func (d *DefaultDispatcher) Subscribe(subscriber Subscriber) {
	subscriber.Subscribe(d)
}

// Dispatch fires an event to all registered listeners
func (d *DefaultDispatcher) Dispatch(event interface{}) error {
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		if listener.ShouldQueue() && d.queue != nil {
			// Queue the listener for async processing
			if err := d.queue.Push(event, listener, 0); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
		} else {
			// Process synchronously
			if err := d.processListener(event, listener); err != nil {
				return err
			}
		}
	}

	return nil
}

// DispatchNow fires an event synchronously
func (d *DefaultDispatcher) DispatchNow(event interface{}) error {
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		if err := d.processListener(event, listener); err != nil {
			return err
		}
	}

	return nil
}

// DispatchAsync fires an event asynchronously
func (d *DefaultDispatcher) DispatchAsync(event interface{}) error {
	if d.queue == nil {
		// Fallback to goroutine if no queue configured
		go func() {
			_ = d.DispatchNow(event)
		}()
		return nil
	}

	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		if err := d.queue.Push(event, listener, 0); err != nil {
			return fmt.Errorf("failed to queue listener: %w", err)
		}
	}

	return nil
}

// DispatchAfter fires an event after a delay
func (d *DefaultDispatcher) DispatchAfter(event interface{}, delay time.Duration) error {
	if d.queue != nil {
		listeners := d.getListenersForEvent(event)
		for _, listener := range listeners {
			if err := d.queue.Push(event, listener, delay); err != nil {
				return fmt.Errorf("failed to queue delayed listener: %w", err)
			}
		}
		return nil
	}

	// Fallback to timer if no queue configured
	time.AfterFunc(delay, func() {
		_ = d.Dispatch(event)
	})

	return nil
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

	delete(d.listeners, event)

	// Also remove matching wildcards
	for pattern := range d.wildcards {
		if d.matchesPattern(event, pattern) {
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

// getListenersForEvent retrieves all listeners for an event
func (d *DefaultDispatcher) getListenersForEvent(event interface{}) []Listener {
	d.mu.RLock()
	defer d.mu.RUnlock()

	eventName := d.getEventName(event)
	var result []Listener

	// Get exact match listeners
	if listeners, ok := d.listeners[eventName]; ok {
		result = append(result, listeners...)
	}

	// Get wildcard listeners
	for pattern, listeners := range d.wildcards {
		if d.matchesPattern(eventName, pattern) {
			result = append(result, listeners...)
		}
	}

	return result
}

// getEventName extracts the event name from various types
func (d *DefaultDispatcher) getEventName(event interface{}) string {
	// If it implements Event interface
	if e, ok := event.(Event); ok {
		return e.Name()
	}

	// If it's already a string
	if s, ok := event.(string); ok {
		return s
	}

	// Use type name as event name
	t := reflect.TypeOf(event)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Convert struct name to event name (e.g., UserRegistered -> user.registered)
	name := t.Name()
	if name == "" {
		name = t.String()
	}

	return camelToSnake(name)
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

// processListener executes a listener
func (d *DefaultDispatcher) processListener(event interface{}, listener Listener) error {
	// Check if listener should handle this event
	if handler, ok := listener.(ShouldHandle); ok {
		if !handler.ShouldHandle(event) {
			return nil
		}
	}

	// Handle the event
	return listener.Handle(event)
}

// camelToSnake converts CamelCase to snake.case
func camelToSnake(s string) string {
	var result strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('.')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}
