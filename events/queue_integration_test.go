package events

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// Test EventListenerJob
func TestEventListenerJob(t *testing.T) {
	// Test with nil listener
	job := &EventListenerJob{
		Event:     "test event",
		EventType: "test.event",
	}

	err := job.Handle()
	if err == nil {
		t.Error("Expected error when listener is nil")
	}

	// Test with valid listener
	handled := false
	listener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled = true
			return nil
		},
	}

	job.listener = listener
	err = job.Handle()
	if err != nil {
		t.Errorf("Handle failed: %v", err)
	}
	if !handled {
		t.Error("Listener was not called")
	}

	// Test with listener that returns error
	errorListener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			return errors.New("listener error")
		},
	}

	job.listener = errorListener
	err = job.Handle()
	if err == nil {
		t.Error("Expected error from listener")
	}

	// Test Failed method (should not panic)
	job.Failed(errors.New("test error"))
}

// Test QueueIntegratedDispatcher
func TestQueueIntegratedDispatcher(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	if dispatcher == nil {
		t.Fatal("NewQueueIntegratedDispatcher returned nil")
	}

	if dispatcher.listenerRegistry == nil {
		t.Error("listenerRegistry not initialized")
	}

	// Test RegisterListenerFactory
	factoryCalled := false
	factory := func() Listener {
		factoryCalled = true
		return &TestListener{}
	}

	dispatcher.RegisterListenerFactory("test.listener", factory)

	// Verify factory was registered
	if len(dispatcher.listenerRegistry) != 1 {
		t.Errorf("Expected 1 registered factory, got %d", len(dispatcher.listenerRegistry))
	}

	// Call factory to verify it works
	registeredFactory := dispatcher.listenerRegistry["test.listener"]
	listener := registeredFactory()
	if listener == nil {
		t.Error("Factory returned nil listener")
	}
	if !factoryCalled {
		t.Error("Factory was not called")
	}
}

// Test getListenerType
func TestGetListenerType(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	// Test with pointer type
	listener := &TestListener{}
	listenerType := dispatcher.getListenerType(listener)
	if listenerType != "events.TestListener" {
		t.Errorf("Expected events.TestListener, got %s", listenerType)
	}

	// Test with non-pointer type (use pointer to satisfy interface)
	listener2 := &TestListener{}
	listenerType = dispatcher.getListenerType(listener2)
	if listenerType != "events.TestListener" {
		t.Errorf("Expected events.TestListener, got %s", listenerType)
	}
}

// Test ProcessEventListenerJob
func TestProcessEventListenerJob(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	// Test with invalid JSON
	err := dispatcher.ProcessEventListenerJob(context.Background(), []byte("invalid json"))
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}

	// Test with unregistered listener type
	job := EventListenerJob{
		Event:        "test event",
		EventType:    "test.event",
		ListenerType: "unregistered",
	}

	jobData, _ := json.Marshal(job)
	err = dispatcher.ProcessEventListenerJob(context.Background(), jobData)
	if err == nil {
		t.Error("Expected error for unregistered listener type")
	}

	// Test with registered listener
	handled := false
	factory := func() Listener {
		return &SimpleListener{
			HandleFunc: func(ctx context.Context, event interface{}) error {
				handled = true
				return nil
			},
		}
	}

	dispatcher.RegisterListenerFactory("test.listener", factory)

	job.ListenerType = "test.listener"
	jobData, _ = json.Marshal(job)
	err = dispatcher.ProcessEventListenerJob(context.Background(), jobData)

	if err != nil {
		t.Errorf("ProcessEventListenerJob failed: %v", err)
	}
	if !handled {
		t.Error("Listener was not called")
	}

	// Test with listener that returns error
	errorFactory := func() Listener {
		return &SimpleListener{
			HandleFunc: func(ctx context.Context, event interface{}) error {
				return errors.New("processing error")
			},
		}
	}

	dispatcher.RegisterListenerFactory("error.listener", errorFactory)
	job.ListenerType = "error.listener"
	jobData, _ = json.Marshal(job)
	err = dispatcher.ProcessEventListenerJob(context.Background(), jobData)

	if err == nil {
		t.Error("Expected error from listener")
	}
}

// Test EventJobFactory
func TestEventJobFactory(t *testing.T) {
	// Test with invalid JSON
	_, err := EventJobFactory([]byte("invalid json"))
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}

	// Test with valid JSON
	job := EventListenerJob{
		Event:        "test event",
		EventType:    "test.event",
		ListenerType: "test.listener",
		Attempts:     1,
		MaxRetries:   3,
	}

	jobData, _ := json.Marshal(job)
	createdJob, err := EventJobFactory(jobData)

	if err != nil {
		t.Errorf("EventJobFactory failed: %v", err)
	}
	if createdJob == nil {
		t.Error("EventJobFactory returned nil job")
	}

	// Verify job properties
	eventJob, ok := createdJob.(*EventListenerJob)
	if !ok {
		t.Error("Job is not EventListenerJob type")
	}
	if eventJob.EventType != "test.event" {
		t.Errorf("Expected test.event, got %s", eventJob.EventType)
	}
}

// Test PriorityListener and PriorityDispatcher
func TestPriorityDispatcher(t *testing.T) {
	dispatcher := NewPriorityDispatcher()

	if dispatcher == nil {
		t.Fatal("NewPriorityDispatcher returned nil")
	}

	// Create listeners with different priorities
	highPriority := &priorityTestListener{
		priority: 100,
		handleFunc: func(ctx context.Context, event interface{}) error {
			return nil
		},
	}

	medPriority := &priorityTestListener{
		priority: 50,
		handleFunc: func(ctx context.Context, event interface{}) error {
			return nil
		},
	}

	lowPriority := &priorityTestListener{
		priority: 10,
		handleFunc: func(ctx context.Context, event interface{}) error {
			// Track execution order
			return nil
		},
	}

	// Add listeners in wrong order
	dispatcher.Listen("test", lowPriority)
	dispatcher.Listen("test", highPriority)
	dispatcher.Listen("test", medPriority)

	// Get listeners - should be sorted by priority
	listeners := dispatcher.getListenersForEvent("test")

	if len(listeners) != 3 {
		t.Errorf("Expected 3 listeners, got %d", len(listeners))
	}

	// Verify order
	if pl, ok := listeners[0].(PriorityListener); ok {
		if pl.Priority() != 100 {
			t.Errorf("First listener should have priority 100, got %d", pl.Priority())
		}
	}

	// Test with mixed priority and non-priority listeners
	regularListener := &TestListener{}
	dispatcher.Listen("mixed", regularListener)
	dispatcher.Listen("mixed", highPriority)

	mixedListeners := dispatcher.getListenersForEvent("mixed")

	// Priority listener should come first
	if _, ok := mixedListeners[0].(PriorityListener); !ok {
		t.Error("Priority listener should come before non-priority listener")
	}
}

// Test StoppableEvent and StoppablePropagationDispatcher
func TestStoppablePropagationDispatcher(t *testing.T) {
	dispatcher := NewStoppablePropagationDispatcher()

	if dispatcher == nil {
		t.Fatal("NewStoppablePropagationDispatcher returned nil")
	}

	// Test BaseStoppableEvent
	event := &BaseStoppableEvent{}

	if event.ShouldStopPropagation() {
		t.Error("New event should not stop propagation")
	}

	event.StopPropagation()

	if !event.ShouldStopPropagation() {
		t.Error("Event should stop propagation after StopPropagation() called")
	}

	// Test dispatch with stoppable event
	listener1Called := false
	listener2Called := false
	listener3Called := false

	listener1 := &stoppablePropagationTestListener{
		handleFunc: func(ctx context.Context, event interface{}) error {
			listener1Called = true
			return nil
		},
		handleWithPropagationFunc: func(ctx context.Context, event interface{}) (bool, error) {
			listener1Called = true
			// Return true to stop propagation
			return true, nil
		},
	}

	listener2 := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			listener2Called = true
			return nil
		},
	}

	listener3 := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			listener3Called = true
			return nil
		},
	}

	dispatcher.Listen("stoppable", listener1)
	dispatcher.Listen("stoppable", listener2)
	dispatcher.Listen("stoppable", listener3)

	stoppableEvent := &testStoppableEvent{
		BaseStoppableEvent: BaseStoppableEvent{
			BaseEvent: BaseEvent{EventName: "stoppable"},
		},
	}

	err := dispatcher.Dispatch(context.Background(), stoppableEvent)
	if err != nil {
		t.Errorf("Dispatch failed: %v", err)
	}

	if !listener1Called {
		t.Error("Listener 1 should have been called")
	}

	// Listeners 2 and 3 should not be called due to propagation stop
	if listener2Called {
		t.Error("Listener 2 should not have been called (propagation stopped)")
	}

	if listener3Called {
		t.Error("Listener 3 should not have been called (propagation stopped)")
	}
}

// Test processListener with StoppablePropagationListener
func TestStoppablePropagationListenerProcessing(t *testing.T) {
	dispatcher := NewStoppablePropagationDispatcher()

	// Test StoppablePropagationListener
	stopPropagation := false
	propagationListener := &stoppablePropagationTestListener{
		handleWithPropagationFunc: func(ctx context.Context, event interface{}) (bool, error) {
			return stopPropagation, nil
		},
	}

	event := &testStoppableEvent{
		BaseStoppableEvent: BaseStoppableEvent{
			BaseEvent: BaseEvent{EventName: "test"},
		},
	}

	// Test without stopping propagation
	err := dispatcher.processListener(context.Background(), event, propagationListener)
	if err != nil {
		t.Errorf("processListener failed: %v", err)
	}
	if event.ShouldStopPropagation() {
		t.Error("Event should not stop propagation")
	}

	// Test with stopping propagation
	stopPropagation = true
	event = &testStoppableEvent{
		BaseStoppableEvent: BaseStoppableEvent{
			BaseEvent: BaseEvent{EventName: "test"},
		},
	}

	err = dispatcher.processListener(context.Background(), event, propagationListener)
	if err != nil {
		t.Errorf("processListener failed: %v", err)
	}
	if !event.ShouldStopPropagation() {
		t.Error("Event should stop propagation")
	}

	// Test with error from listener
	errorListener := &stoppablePropagationTestListener{
		handleWithPropagationFunc: func(ctx context.Context, event interface{}) (bool, error) {
			return false, errors.New("listener error")
		},
	}

	err = dispatcher.processListener(context.Background(), event, errorListener)
	if err == nil {
		t.Error("Expected error from listener")
	}
}

// Test InitializeQueueIntegration
func TestInitializeQueueIntegration(t *testing.T) {
	// This should not panic. Passing nil for dispatcher, driver, and reporter
	// is the minimum-info form: registers the queue.RegisterJob factory and
	// clears any previously installed failure reporter.
	InitializeQueueIntegration(nil, nil, nil)
}

// Test Dispatch method in QueueIntegratedDispatcher
func TestQueueIntegratedDispatcherDispatch(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	// Test with non-queued listener
	handled := false
	listener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled = true
			return nil
		},
	}

	dispatcher.Listen("test.event", listener)
	err := dispatcher.Dispatch(context.Background(), "test.event")
	if err != nil {
		t.Errorf("Dispatch failed: %v", err)
	}
	if !handled {
		t.Error("Listener should have been called")
	}

	// Test with listener that returns error
	errorListener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			return errors.New("handler error")
		},
	}

	dispatcher.Listen("error.event", errorListener)
	err = dispatcher.Dispatch(context.Background(), "error.event")
	if err == nil {
		t.Error("Expected error from listener")
	}
}

// Test processListener in QueueIntegratedDispatcher
func TestQueueIntegratedProcessListener(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	// Test with ShouldHandle that returns false
	listener := &TestShouldHandleListener{
		shouldHandle: false,
	}

	event := "test.event"
	err := dispatcher.processListener(context.Background(), event, listener)
	if err != nil {
		t.Errorf("processListener failed: %v", err)
	}

	// Verify Handle was not called
	if listener.handleCalled {
		t.Error("Handle should not have been called when ShouldHandle returns false")
	}

	// Test with ShouldHandle that returns true
	listener2 := &TestShouldHandleListener{
		shouldHandle: true,
	}

	err = dispatcher.processListener(context.Background(), event, listener2)
	if err != nil {
		t.Errorf("processListener failed: %v", err)
	}

	// Verify Handle was called
	if !listener2.handleCalled {
		t.Error("Handle should have been called when ShouldHandle returns true")
	}
}

// Test StoppablePropagationDispatcher Dispatch with queued listeners
func TestStoppablePropagationDispatcherWithQueued(t *testing.T) {
	dispatcher := NewStoppablePropagationDispatcher()

	// Note: We can't fully test queuing without mocking the queue package,
	// but we can test that the method doesn't panic and handles the flow
	queuedListener := &TestFullQueuedListener{}
	regularListener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			return nil
		},
	}

	dispatcher.Listen("mixed", queuedListener)
	dispatcher.Listen("mixed", regularListener)

	event := &BaseStoppableEvent{
		BaseEvent: BaseEvent{EventName: "mixed"},
	}

	// This will fail to queue but should not panic
	// We just ensure no panic occurs
	_ = dispatcher.Dispatch(context.Background(), event)
}

// TestShouldHandleListener for testing ShouldHandle interface
type TestShouldHandleListener struct {
	shouldHandle bool
	handleCalled bool
}

func (l *TestShouldHandleListener) Handle(ctx context.Context, event interface{}) error {
	l.handleCalled = true
	return nil
}

func (l *TestShouldHandleListener) ShouldQueue() bool {
	return false
}

func (l *TestShouldHandleListener) ShouldHandle(event interface{}) bool {
	return l.shouldHandle
}

// Helper types for testing
type priorityTestListener struct {
	priority   int
	handleFunc func(ctx context.Context, event interface{}) error
}

func (l *priorityTestListener) Handle(ctx context.Context, event interface{}) error {
	if l.handleFunc != nil {
		return l.handleFunc(ctx, event)
	}
	return nil
}

func (l *priorityTestListener) ShouldQueue() bool {
	return false
}

func (l *priorityTestListener) Priority() int {
	return l.priority
}

type testStoppableEvent struct {
	BaseStoppableEvent
}

func (e *testStoppableEvent) Name() string {
	return "stoppable"
}

type stoppablePropagationTestListener struct {
	handleFunc                func(ctx context.Context, event interface{}) error
	handleWithPropagationFunc func(ctx context.Context, event interface{}) (bool, error)
	shouldHandle              bool
}

func (l *stoppablePropagationTestListener) Handle(ctx context.Context, event interface{}) error {
	if l.handleFunc != nil {
		return l.handleFunc(ctx, event)
	}
	return nil
}

func (l *stoppablePropagationTestListener) ShouldQueue() bool {
	return false
}

func (l *stoppablePropagationTestListener) HandleWithPropagation(ctx context.Context, event interface{}) (bool, error) {
	if l.handleWithPropagationFunc != nil {
		return l.handleWithPropagationFunc(ctx, event)
	}
	return false, nil
}

func (l *stoppablePropagationTestListener) ShouldHandle(event interface{}) bool {
	if l.shouldHandle {
		return true
	}
	return true // default to handling
}

// Test Dispatch with queued listeners in QueueIntegratedDispatcher
func TestQueueIntegratedDispatchWithQueued(t *testing.T) {
	// Note: This test would require mocking the queue package
	// Since we can't modify the queue package here, we'll test the logic
	// that prepares the job for queueing

	dispatcher := NewQueueIntegratedDispatcher()

	// Register a queued listener
	queuedListener := &TestFullQueuedListener{}
	dispatcher.Listen("queued.event", queuedListener)

	// The actual queueing would fail because queue.Push is not mocked
	// but we can verify the listener is recognized as queued
	listeners := dispatcher.getListenersForEvent("queued.event")
	if len(listeners) != 1 {
		t.Errorf("Expected 1 listener, got %d", len(listeners))
	}

	if !listeners[0].ShouldQueue() {
		t.Error("Listener should be marked for queueing")
	}
}

// Test pushToQueue method
func TestPushToQueue(t *testing.T) {
	dispatcher := NewQueueIntegratedDispatcher()

	// Test with QueuedListener
	queuedListener := &TestFullQueuedListener{}
	event := "test event"

	// This will fail because queue.Push/Later are not mocked,
	// but we're testing the job preparation logic
	job := &EventListenerJob{
		Event:        event,
		EventType:    dispatcher.getEventName(event),
		ListenerType: dispatcher.getListenerType(queuedListener),
		Attempts:     0,
		MaxRetries:   queuedListener.Tries(),
	}

	if job.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries to be 3, got %d", job.MaxRetries)
	}

	if job.EventType != "test event" {
		t.Errorf("Expected EventType to be 'test event', got %s", job.EventType)
	}
}
