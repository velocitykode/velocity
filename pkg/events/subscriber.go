package events

import (
	"fmt"
	"reflect"
	"strings"
)

// SubscriberDispatcher extends the base dispatcher with subscriber support
type SubscriberDispatcher struct {
	*DefaultDispatcher
	subscribers []Subscriber
}

// NewSubscriberDispatcher creates a new dispatcher with subscriber support
func NewSubscriberDispatcher() *SubscriberDispatcher {
	return &SubscriberDispatcher{
		DefaultDispatcher: NewDispatcher(),
		subscribers:       make([]Subscriber, 0),
	}
}

// Subscribe registers a subscriber with the dispatcher
func (d *SubscriberDispatcher) Subscribe(subscriber Subscriber) {
	d.mu.Lock()
	d.subscribers = append(d.subscribers, subscriber)
	d.mu.Unlock()

	// Register all listeners from the subscriber
	subscriber.Subscribe(d)
}

// GetSubscribers returns all registered subscribers
func (d *SubscriberDispatcher) GetSubscribers() []Subscriber {
	d.mu.RLock()
	defer d.mu.RUnlock()

	result := make([]Subscriber, len(d.subscribers))
	copy(result, d.subscribers)
	return result
}

// AutoSubscriber provides automatic event registration based on method names
type AutoSubscriber struct {
	instance interface{}
	prefix   string
}

// NewAutoSubscriber creates a subscriber that auto-registers based on method names
// Methods should follow the pattern: HandleEventName(event interface{}) error
func NewAutoSubscriber(instance interface{}, prefix string) *AutoSubscriber {
	if prefix == "" {
		prefix = "Handle"
	}
	return &AutoSubscriber{
		instance: instance,
		prefix:   prefix,
	}
}

// Subscribe auto-registers methods as event listeners
func (s *AutoSubscriber) Subscribe(dispatcher Dispatcher) {
	instanceType := reflect.TypeOf(s.instance)
	instanceValue := reflect.ValueOf(s.instance)

	// Iterate through all methods
	for i := 0; i < instanceType.NumMethod(); i++ {
		method := instanceType.Method(i)

		// Check if method name starts with prefix (e.g., "Handle")
		if !strings.HasPrefix(method.Name, s.prefix) {
			continue
		}

		// Extract event name from method name
		eventName := s.extractEventName(method.Name)
		if eventName == "" {
			continue
		}

		// Verify method signature: func(event interface{}) error
		if !s.isValidHandlerSignature(method.Type) {
			continue
		}

		// Create a listener wrapper for this method
		methodValue := instanceValue.Method(i)
		listener := &methodListener{
			handler: func(event interface{}) error {
				results := methodValue.Call([]reflect.Value{reflect.ValueOf(event)})
				if len(results) > 0 && !results[0].IsNil() {
					return results[0].Interface().(error)
				}
				return nil
			},
			name: method.Name,
		}

		// Register the listener
		dispatcher.Listen(eventName, listener)
	}
}

// extractEventName converts method name to event name
// HandleUserRegistered -> user.registered
// HandleOrderPlaced -> order.placed
func (s *AutoSubscriber) extractEventName(methodName string) string {
	if !strings.HasPrefix(methodName, s.prefix) {
		return ""
	}

	// Remove prefix
	name := strings.TrimPrefix(methodName, s.prefix)
	if name == "" {
		return ""
	}

	// Convert CamelCase to dot notation
	var result strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result.WriteRune('.')
		}
		result.WriteRune(r)
	}
	return strings.ToLower(result.String())
}

// isValidHandlerSignature checks if method has correct signature
func (s *AutoSubscriber) isValidHandlerSignature(methodType reflect.Type) bool {
	// Should have exactly 2 params (receiver + event)
	if methodType.NumIn() != 2 {
		return false
	}

	// Second param should be interface{} or a concrete type
	// We accept any type since events can be strongly typed

	// Should return exactly 1 value (error)
	if methodType.NumOut() != 1 {
		return false
	}

	// Return type should be error
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	return methodType.Out(0) == errorType
}

// methodListener wraps a method as a Listener
type methodListener struct {
	handler func(interface{}) error
	name    string
}

func (l *methodListener) Handle(event interface{}) error {
	return l.handler(event)
}

func (l *methodListener) ShouldQueue() bool {
	return false
}

// EventMap allows explicit event mapping for subscribers
type EventMap map[string]string // method name -> event name

// MappedSubscriber allows explicit mapping of methods to events
type MappedSubscriber struct {
	instance interface{}
	mappings EventMap
}

// NewMappedSubscriber creates a subscriber with explicit event mappings
func NewMappedSubscriber(instance interface{}, mappings EventMap) *MappedSubscriber {
	return &MappedSubscriber{
		instance: instance,
		mappings: mappings,
	}
}

// Subscribe registers methods according to the mapping
func (s *MappedSubscriber) Subscribe(dispatcher Dispatcher) {
	instanceType := reflect.TypeOf(s.instance)
	instanceValue := reflect.ValueOf(s.instance)

	for methodName, eventName := range s.mappings {
		// Find the method
		method, ok := instanceType.MethodByName(methodName)
		if !ok {
			// Skip if method doesn't exist
			continue
		}

		// Create listener wrapper
		methodValue := instanceValue.MethodByName(methodName)
		if !methodValue.IsValid() {
			continue
		}

		listener := &methodListener{
			handler: func(event interface{}) error {
				results := methodValue.Call([]reflect.Value{reflect.ValueOf(event)})
				if len(results) > 0 && !results[0].IsNil() {
					return results[0].Interface().(error)
				}
				return nil
			},
			name: method.Name,
		}

		// Register the listener
		dispatcher.Listen(eventName, listener)
	}
}

// SubscriberGroup allows grouping multiple subscribers
type SubscriberGroup struct {
	subscribers []Subscriber
}

// NewSubscriberGroup creates a new subscriber group
func NewSubscriberGroup(subscribers ...Subscriber) *SubscriberGroup {
	return &SubscriberGroup{
		subscribers: subscribers,
	}
}

// Add adds a subscriber to the group
func (g *SubscriberGroup) Add(subscriber Subscriber) {
	g.subscribers = append(g.subscribers, subscriber)
}

// Subscribe registers all subscribers in the group
func (g *SubscriberGroup) Subscribe(dispatcher Dispatcher) {
	for _, subscriber := range g.subscribers {
		subscriber.Subscribe(dispatcher)
	}
}

// QueuedMethodListener wraps a method as a queued listener
type QueuedMethodListener struct {
	methodListener
	queueName string
	delay     int
	tries     int
}

// ShouldQueue returns true for queued listeners
func (l *QueuedMethodListener) ShouldQueue() bool {
	return true
}

// OnQueue returns the queue name
func (l *QueuedMethodListener) OnQueue() string {
	if l.queueName == "" {
		return "default"
	}
	return l.queueName
}

// WithDelay returns the delay in seconds
func (l *QueuedMethodListener) WithDelay() int {
	return l.delay
}

// Tries returns the number of retry attempts
func (l *QueuedMethodListener) Tries() int {
	if l.tries == 0 {
		return 3
	}
	return l.tries
}

// SubscriberError represents an error during subscription
type SubscriberError struct {
	Subscriber string
	Method     string
	Err        error
}

func (e *SubscriberError) Error() string {
	return fmt.Sprintf("subscriber %s method %s: %v", e.Subscriber, e.Method, e.Err)
}

// ValidateSubscriber validates that a subscriber has valid method signatures
func ValidateSubscriber(subscriber interface{}) []error {
	var errors []error
	subscriberType := reflect.TypeOf(subscriber)

	// Check for Handle* methods
	for i := 0; i < subscriberType.NumMethod(); i++ {
		method := subscriberType.Method(i)

		if strings.HasPrefix(method.Name, "Handle") {
			// Validate signature
			if method.Type.NumIn() != 2 {
				errors = append(errors, &SubscriberError{
					Subscriber: subscriberType.Name(),
					Method:     method.Name,
					Err:        fmt.Errorf("invalid parameter count: expected 2, got %d", method.Type.NumIn()),
				})
				continue
			}

			if method.Type.NumOut() != 1 {
				errors = append(errors, &SubscriberError{
					Subscriber: subscriberType.Name(),
					Method:     method.Name,
					Err:        fmt.Errorf("invalid return count: expected 1, got %d", method.Type.NumOut()),
				})
				continue
			}

			// Check return type is error
			errorType := reflect.TypeOf((*error)(nil)).Elem()
			if method.Type.Out(0) != errorType {
				errors = append(errors, &SubscriberError{
					Subscriber: subscriberType.Name(),
					Method:     method.Name,
					Err:        fmt.Errorf("invalid return type: expected error, got %v", method.Type.Out(0)),
				})
			}
		}
	}

	return errors
}
