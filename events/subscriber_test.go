package events

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// Test subscriber that implements multiple event handlers
type TestOrderSubscriber struct {
	mu             sync.Mutex
	orderPlaced    bool
	orderShipped   bool
	orderCancelled bool
	orderRefunded  bool
	placedEvent    interface{}
	shippedEvent   interface{}
	cancelledEvent interface{}
	refundedEvent  interface{}
}

func (s *TestOrderSubscriber) HandleOrderPlaced(event interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderPlaced = true
	s.placedEvent = event
	return nil
}

func (s *TestOrderSubscriber) HandleOrderShipped(event interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderShipped = true
	s.shippedEvent = event
	return nil
}

func (s *TestOrderSubscriber) HandleOrderCancelled(event interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderCancelled = true
	s.cancelledEvent = event
	return nil
}

func (s *TestOrderSubscriber) HandleOrderRefunded(event interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orderRefunded = true
	s.refundedEvent = event
	return nil
}

// Invalid handler - wrong signature
func (s *TestOrderSubscriber) HandleInvalid() {
	// No parameters, no return - invalid
}

// Another invalid handler - wrong return type
func (s *TestOrderSubscriber) HandleAlsoInvalid(event interface{}) string {
	return "invalid"
}

// Manual subscriber implementation
type ManualSubscriber struct {
	listeners map[string]Listener
}

func NewManualSubscriber() *ManualSubscriber {
	return &ManualSubscriber{
		listeners: make(map[string]Listener),
	}
}

func (s *ManualSubscriber) Subscribe(dispatcher Dispatcher) {
	for event, listener := range s.listeners {
		dispatcher.Listen(event, listener)
	}
}

func TestSubscriberDispatcher(t *testing.T) {
	dispatcher := NewSubscriberDispatcher()

	// Create a manual subscriber
	subscriber := NewManualSubscriber()
	handled := false
	subscriber.listeners["test.event"] = &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled = true
			return nil
		},
	}

	// Subscribe
	dispatcher.Subscribe(subscriber)

	// Dispatch event
	err := dispatcher.Dispatch(context.Background(), "test.event")
	if err != nil {
		t.Errorf("Dispatch failed: %v", err)
	}

	if !handled {
		t.Error("Subscriber's listener was not called")
	}

	// Check subscribers list
	subscribers := dispatcher.GetSubscribers()
	if len(subscribers) != 1 {
		t.Errorf("Expected 1 subscriber, got %d", len(subscribers))
	}
}

func TestAutoSubscriber(t *testing.T) {
	dispatcher := NewDispatcher()
	subscriber := &TestOrderSubscriber{}

	// Create auto subscriber
	autoSub := NewAutoSubscriber(subscriber, "Handle")

	// Subscribe
	autoSub.Subscribe(dispatcher)

	// Dispatch events
	dispatcher.Dispatch(context.Background(), "order.placed")
	dispatcher.Dispatch(context.Background(), "order.shipped")
	dispatcher.Dispatch(context.Background(), "order.cancelled")

	// Check handlers were called
	subscriber.mu.Lock()
	defer subscriber.mu.Unlock()

	if !subscriber.orderPlaced {
		t.Error("HandleOrderPlaced was not called")
	}
	if !subscriber.orderShipped {
		t.Error("HandleOrderShipped was not called")
	}
	if !subscriber.orderCancelled {
		t.Error("HandleOrderCancelled was not called")
	}
	if subscriber.orderRefunded {
		t.Error("HandleOrderRefunded should not have been called")
	}
}

func TestAutoSubscriberEventNameExtraction(t *testing.T) {
	// extractEventName is pure string manipulation — any struct instance is
	// fine for exercising it.
	autoSub := NewAutoSubscriber(struct{}{}, "Handle")

	tests := []struct {
		methodName string
		expected   string
	}{
		{"HandleUserRegistered", "user.registered"},
		{"HandleOrderPlaced", "order.placed"},
		{"HandlePaymentProcessed", "payment.processed"},
		{"HandleUserProfileUpdated", "user.profile.updated"},
		{"NotHandle", ""},
		{"Handle", ""},
		{"", ""},
	}

	for _, test := range tests {
		result := autoSub.extractEventName(test.methodName)
		if result != test.expected {
			t.Errorf("extractEventName(%s) = %s, expected %s",
				test.methodName, result, test.expected)
		}
	}
}

func TestMappedSubscriber(t *testing.T) {
	dispatcher := NewDispatcher()
	subscriber := &TestOrderSubscriber{}

	// Create mapped subscriber with explicit mappings
	mappings := EventMap{
		"HandleOrderPlaced":    "custom.order.new",
		"HandleOrderShipped":   "custom.order.sent",
		"HandleOrderCancelled": "custom.order.void",
	}

	mappedSub := NewMappedSubscriber(subscriber, mappings)

	// Subscribe
	mappedSub.Subscribe(dispatcher)

	// Dispatch with custom event names
	dispatcher.Dispatch(context.Background(), "custom.order.new")
	dispatcher.Dispatch(context.Background(), "custom.order.sent")
	dispatcher.Dispatch(context.Background(), "custom.order.void")

	// Check handlers were called
	subscriber.mu.Lock()
	defer subscriber.mu.Unlock()

	if !subscriber.orderPlaced {
		t.Error("HandleOrderPlaced was not called")
	}
	if !subscriber.orderShipped {
		t.Error("HandleOrderShipped was not called")
	}
	if !subscriber.orderCancelled {
		t.Error("HandleOrderCancelled was not called")
	}
}

func TestSubscriberGroup(t *testing.T) {
	dispatcher := NewDispatcher()

	// Create multiple subscribers
	sub1 := NewManualSubscriber()
	handled1 := false
	sub1.listeners["event1"] = &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled1 = true
			return nil
		},
	}

	sub2 := NewManualSubscriber()
	handled2 := false
	sub2.listeners["event2"] = &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled2 = true
			return nil
		},
	}

	// Create group
	group := NewSubscriberGroup(sub1, sub2)

	// Add another subscriber
	sub3 := NewManualSubscriber()
	handled3 := false
	sub3.listeners["event3"] = &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled3 = true
			return nil
		},
	}
	group.Add(sub3)

	// Subscribe group
	group.Subscribe(dispatcher)

	// Dispatch events
	dispatcher.Dispatch(context.Background(), "event1")
	dispatcher.Dispatch(context.Background(), "event2")
	dispatcher.Dispatch(context.Background(), "event3")

	// Check all were called
	if !handled1 {
		t.Error("Subscriber 1 was not called")
	}
	if !handled2 {
		t.Error("Subscriber 2 was not called")
	}
	if !handled3 {
		t.Error("Subscriber 3 was not called")
	}
}

func TestValidateSubscriber(t *testing.T) {
	subscriber := &TestOrderSubscriber{}

	errors := ValidateSubscriber(subscriber)

	// Should have 2 errors for invalid methods
	expectedErrors := 2
	if len(errors) != expectedErrors {
		t.Errorf("Expected %d validation errors, got %d", expectedErrors, len(errors))
	}

	// Check error messages
	for _, err := range errors {
		subErr, ok := err.(*SubscriberError)
		if !ok {
			t.Error("Error is not a SubscriberError")
			continue
		}

		if subErr.Method != "HandleInvalid" && subErr.Method != "HandleAlsoInvalid" {
			t.Errorf("Unexpected invalid method: %s", subErr.Method)
		}
	}
}

// ctxAwareSubscriber declares Handle* methods that accept ctx as the first
// parameter, the preferred shape after the ctx-on-Subscribe sweep.
type ctxAwareSubscriber struct{}

func (ctxAwareSubscriber) HandleUserRegistered(ctx context.Context, event interface{}) error {
	return nil
}

func (ctxAwareSubscriber) HandleOrderPlaced(ctx context.Context, event interface{}) error {
	return nil
}

// invalidCtxSubscriber declares a 3-arg method whose first parameter is not
// context.Context, which must be flagged.
type invalidCtxSubscriber struct{}

func (invalidCtxSubscriber) HandleBadCtx(notCtx string, event interface{}) error { return nil }

// TestValidateSubscriber_AcceptsCtxAwareMethods locks in the post-sweep
// behaviour: Handle* methods declared as (ctx, event) error must validate
// without errors. Before the fix, ValidateSubscriber accepted only the
// legacy 2-arg shape, which would have produced spurious errors here.
func TestValidateSubscriber_AcceptsCtxAwareMethods(t *testing.T) {
	if errs := ValidateSubscriber(ctxAwareSubscriber{}); len(errs) != 0 {
		t.Errorf("ctx-aware subscriber should validate cleanly, got %d errors: %v", len(errs), errs)
	}
}

// TestValidateSubscriber_RejectsNonCtxFirstArg ensures a 3-arg method whose
// first parameter is not context.Context is still flagged so accidental
// signatures don't slip through.
func TestValidateSubscriber_RejectsNonCtxFirstArg(t *testing.T) {
	errs := ValidateSubscriber(invalidCtxSubscriber{})
	if len(errs) != 1 {
		t.Fatalf("expected 1 error for non-ctx first arg, got %d: %v", len(errs), errs)
	}
	subErr, ok := errs[0].(*SubscriberError)
	if !ok || subErr.Method != "HandleBadCtx" {
		t.Errorf("expected SubscriberError for HandleBadCtx, got %v", errs[0])
	}
}

// Test subscriber with error
type ErrorSubscriber struct {
	shouldError bool
}

func (s *ErrorSubscriber) HandleEvent(event interface{}) error {
	if s.shouldError {
		return errors.New("handler error")
	}
	return nil
}

func TestAutoSubscriberWithError(t *testing.T) {
	dispatcher := NewDispatcher()
	subscriber := &ErrorSubscriber{shouldError: true}

	// Create auto subscriber
	autoSub := NewAutoSubscriber(subscriber, "Handle")

	// Subscribe
	autoSub.Subscribe(dispatcher)

	// Dispatch event - should return error
	err := dispatcher.Dispatch(context.Background(), "event")
	if err == nil {
		t.Error("Expected error from handler")
	}
}

func TestMethodListener(t *testing.T) {
	called := false
	var receivedEvent interface{}

	listener := &methodListener{
		handler: func(ctx context.Context, event interface{}) error {
			called = true
			receivedEvent = event
			return nil
		},
		name: "TestMethod",
	}

	// Test Handle
	event := "test event"
	err := listener.Handle(context.Background(), event)
	if err != nil {
		t.Errorf("Handle failed: %v", err)
	}

	if !called {
		t.Error("Handler was not called")
	}

	if receivedEvent != event {
		t.Error("Event was not passed correctly")
	}

	// Test ShouldQueue
	if listener.ShouldQueue() {
		t.Error("Method listener should not be queued by default")
	}
}

func TestQueuedMethodListener(t *testing.T) {
	listener := &QueuedMethodListener{
		methodListener: methodListener{
			handler: func(ctx context.Context, event interface{}) error {
				return nil
			},
			name: "TestMethod",
		},
		queueName: "custom",
		delay:     10,
		tries:     5,
	}

	// Test ShouldQueue
	if !listener.ShouldQueue() {
		t.Error("Queued listener should return true for ShouldQueue")
	}

	// Test OnQueue
	if listener.OnQueue() != "custom" {
		t.Errorf("OnQueue() = %s, expected custom", listener.OnQueue())
	}

	// Test WithDelay
	if listener.WithDelay() != 10 {
		t.Errorf("WithDelay() = %d, expected 10", listener.WithDelay())
	}

	// Test Tries
	if listener.Tries() != 5 {
		t.Errorf("Tries() = %d, expected 5", listener.Tries())
	}

	// Test defaults
	defaultListener := &QueuedMethodListener{}
	if defaultListener.OnQueue() != "default" {
		t.Error("Default queue should be 'default'")
	}
	if defaultListener.Tries() != 3 {
		t.Error("Default tries should be 3")
	}
}

func TestSubscriberError(t *testing.T) {
	err := &SubscriberError{
		Subscriber: "TestSubscriber",
		Method:     "HandleEvent",
		Err:        errors.New("invalid signature"),
	}

	expected := "subscriber TestSubscriber method HandleEvent: invalid signature"
	if err.Error() != expected {
		t.Errorf("Error message = %s, expected %s", err.Error(), expected)
	}
}

// Benchmark tests
func BenchmarkAutoSubscriber(b *testing.B) {
	dispatcher := NewDispatcher()
	subscriber := &TestOrderSubscriber{}
	autoSub := NewAutoSubscriber(subscriber, "Handle")
	autoSub.Subscribe(dispatcher)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), "order.placed")
	}
}

func BenchmarkMappedSubscriber(b *testing.B) {
	dispatcher := NewDispatcher()
	subscriber := &TestOrderSubscriber{}

	mappings := EventMap{
		"HandleOrderPlaced": "order.placed",
	}
	mappedSub := NewMappedSubscriber(subscriber, mappings)
	mappedSub.Subscribe(dispatcher)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), "order.placed")
	}
}
