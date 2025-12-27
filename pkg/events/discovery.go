package events

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// EventRegistry provides event and listener registration
type EventRegistry struct {
	mu        sync.RWMutex
	listeners map[string][]string // event name -> listener names
	providers []EventProvider
}

// NewEventRegistry creates a new event registry
func NewEventRegistry() *EventRegistry {
	return &EventRegistry{
		listeners: make(map[string][]string),
		providers: make([]EventProvider, 0),
	}
}

// Register registers a listener for an event
func (r *EventRegistry) Register(eventName string, listenerName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners[eventName] = append(r.listeners[eventName], listenerName)
}

// GetListeners returns all listeners for an event
func (r *EventRegistry) GetListeners(eventName string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.listeners[eventName]
}

// GetAllEvents returns all registered event names
func (r *EventRegistry) GetAllEvents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	events := make([]string, 0, len(r.listeners))
	for event := range r.listeners {
		events = append(events, event)
	}
	return events
}

// DiscoverFromType discovers event mappings from a subscriber type
func (r *EventRegistry) DiscoverFromType(subscriber interface{}) map[string]string {
	discovered := make(map[string]string)

	val := reflect.ValueOf(subscriber)
	typ := val.Type()

	// Handle pointer types
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
		val = val.Elem()
	}

	typeName := typ.Name()

	// Check pointer receiver methods
	ptrType := reflect.PtrTo(typ)
	for i := 0; i < ptrType.NumMethod(); i++ {
		method := ptrType.Method(i)
		if strings.HasPrefix(method.Name, "Handle") {
			eventName := extractEventName(method.Name)
			if eventName != "" {
				listenerName := fmt.Sprintf("%s.%s", typeName, method.Name)
				discovered[eventName] = listenerName
				r.Register(eventName, listenerName)
			}
		}
	}

	return discovered
}

// extractEventName converts HandleUserRegistered -> user.registered
func extractEventName(methodName string) string {
	if !strings.HasPrefix(methodName, "Handle") {
		return ""
	}

	name := strings.TrimPrefix(methodName, "Handle")
	if name == "" {
		return ""
	}

	var result strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('.')
		}
		result.WriteRune(r)
	}

	return strings.ToLower(result.String())
}

// EventProvider provides event registration
type EventProvider interface {
	// Register registers events with the dispatcher
	Register(dispatcher Dispatcher)
}

// AddProvider adds an event provider
func (r *EventRegistry) AddProvider(provider EventProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers = append(r.providers, provider)
}

// BootProviders boots all registered providers
func (r *EventRegistry) BootProviders(dispatcher Dispatcher) {
	r.mu.RLock()
	providers := make([]EventProvider, len(r.providers))
	copy(providers, r.providers)
	r.mu.RUnlock()

	for _, provider := range providers {
		provider.Register(dispatcher)
	}
}

// Clear clears all registrations
func (r *EventRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = make(map[string][]string)
	r.providers = make([]EventProvider, 0)
}

// Count returns the total number of listener registrations
func (r *EventRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, listeners := range r.listeners {
		count += len(listeners)
	}
	return count
}
