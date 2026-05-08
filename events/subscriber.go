package events

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"
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

// NewAutoSubscriber creates a subscriber that auto-registers based on method names.
// Methods should follow the pattern: HandleEventName(event interface{}) error.
// instance must be a struct or a pointer to a struct; other kinds (map, slice,
// interface, func, chan, primitive) cause a panic because reflection-based
// method discovery is meaningless on them.
func NewAutoSubscriber(instance interface{}, prefix string) *AutoSubscriber {
	if prefix == "" {
		prefix = "Handle"
	}
	if err := ensureStructInstance(instance); err != nil {
		panic(fmt.Errorf("velocity/events: NewAutoSubscriber: %w", err))
	}
	return &AutoSubscriber{
		instance: instance,
		prefix:   prefix,
	}
}

// ensureStructInstance verifies that instance is a struct or *struct.
// Returns a lowercase-message error when the kind is anything else.
func ensureStructInstance(instance interface{}) error {
	if instance == nil {
		return fmt.Errorf("instance is nil")
	}
	t := reflect.TypeOf(instance)
	switch t.Kind() {
	case reflect.Struct:
		return nil
	case reflect.Pointer:
		if t.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("instance kind *%s is not *struct", t.Elem().Kind())
		}
		return nil
	default:
		return fmt.Errorf("instance kind %s is not struct or *struct", t.Kind())
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

		// Verify method signature: func(ctx, event interface{}) error or
		// the legacy func(event interface{}) error.
		methodType := method.Type
		isCtxAware, ok := s.classifyHandlerSignature(methodType)
		if !ok {
			continue
		}

		// Create a listener wrapper for this method
		methodValue := instanceValue.Method(i)
		listener := &methodListener{
			handler: func(ctx context.Context, event interface{}) error {
				var args []reflect.Value
				if isCtxAware {
					args = []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(event)}
				} else {
					args = []reflect.Value{reflect.ValueOf(event)}
				}
				results := methodValue.Call(args)
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

// classifyHandlerSignature checks if the method has a supported signature and
// reports whether the first user param is ctx. Supported shapes:
//   - func(receiver, event) error              (legacy, isCtxAware=false)
//   - func(receiver, ctx, event) error         (preferred, isCtxAware=true)
func (s *AutoSubscriber) classifyHandlerSignature(methodType reflect.Type) (isCtxAware, ok bool) {
	if methodType.NumOut() != 1 {
		return false, false
	}
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	if methodType.Out(0) != errorType {
		return false, false
	}
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()
	switch methodType.NumIn() {
	case 2:
		// (receiver, event)
		return false, true
	case 3:
		// (receiver, ctx, event)
		if methodType.In(1) == ctxType || methodType.In(1).Implements(ctxType) {
			return true, true
		}
		return false, false
	default:
		return false, false
	}
}

// methodListener wraps a method as a Listener
type methodListener struct {
	handler func(ctx context.Context, event interface{}) error
	name    string
}

func (l *methodListener) Handle(ctx context.Context, event interface{}) error {
	return l.handler(ctx, event)
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

// NewMappedSubscriber creates a subscriber with explicit event mappings.
// instance must be a struct or a pointer to a struct — see
// NewAutoSubscriber for rationale.
func NewMappedSubscriber(instance interface{}, mappings EventMap) *MappedSubscriber {
	if err := ensureStructInstance(instance); err != nil {
		panic(fmt.Errorf("velocity/events: NewMappedSubscriber: %w", err))
	}
	return &MappedSubscriber{
		instance: instance,
		mappings: mappings,
	}
}

// Subscribe registers methods according to the mapping
func (s *MappedSubscriber) Subscribe(dispatcher Dispatcher) {
	instanceType := reflect.TypeOf(s.instance)
	instanceValue := reflect.ValueOf(s.instance)

	auto := AutoSubscriber{} // borrow signature classifier
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

		isCtxAware, ok := auto.classifyHandlerSignature(method.Type)
		if !ok {
			continue
		}

		listener := &methodListener{
			handler: func(ctx context.Context, event interface{}) error {
				var args []reflect.Value
				if isCtxAware {
					args = []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(event)}
				} else {
					args = []reflect.Value{reflect.ValueOf(event)}
				}
				results := methodValue.Call(args)
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
	delay     time.Duration
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

// WithDelay returns the delay before processing
func (l *QueuedMethodListener) WithDelay() time.Duration {
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

// ValidateSubscriber validates that a subscriber has valid Handle*-prefixed
// method signatures. Both the legacy form (receiver, event) and the
// preferred ctx-aware form (receiver, ctx, event) are accepted, mirroring
// AutoSubscriber.classifyHandlerSignature so a subscriber that ships the
// ctx-aware methods is no longer flagged as invalid.
func ValidateSubscriber(subscriber interface{}) []error {
	var errs []error
	subscriberType := reflect.TypeOf(subscriber)
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	ctxType := reflect.TypeOf((*context.Context)(nil)).Elem()

	for i := 0; i < subscriberType.NumMethod(); i++ {
		method := subscriberType.Method(i)
		if !strings.HasPrefix(method.Name, "Handle") {
			continue
		}

		mt := method.Type
		// Accept either:
		//   func(receiver, event) error              (legacy, NumIn==2)
		//   func(receiver, ctx, event) error         (preferred, NumIn==3)
		switch mt.NumIn() {
		case 2:
			// legacy shape, no further structural check.
		case 3:
			if mt.In(1) != ctxType && !mt.In(1).Implements(ctxType) {
				errs = append(errs, &SubscriberError{
					Subscriber: subscriberType.Name(),
					Method:     method.Name,
					Err:        fmt.Errorf("invalid first parameter: expected context.Context, got %v", mt.In(1)),
				})
				continue
			}
		default:
			errs = append(errs, &SubscriberError{
				Subscriber: subscriberType.Name(),
				Method:     method.Name,
				Err:        fmt.Errorf("invalid parameter count: expected 2 or 3 (with ctx), got %d", mt.NumIn()),
			})
			continue
		}

		if mt.NumOut() != 1 {
			errs = append(errs, &SubscriberError{
				Subscriber: subscriberType.Name(),
				Method:     method.Name,
				Err:        fmt.Errorf("invalid return count: expected 1, got %d", mt.NumOut()),
			})
			continue
		}

		if mt.Out(0) != errorType {
			errs = append(errs, &SubscriberError{
				Subscriber: subscriberType.Name(),
				Method:     method.Name,
				Err:        fmt.Errorf("invalid return type: expected error, got %v", mt.Out(0)),
			})
		}
	}

	return errs
}
