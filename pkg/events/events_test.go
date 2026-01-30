package events

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Test event types
type UserRegistered struct {
	UserID int
	Email  string
}

func (e UserRegistered) Name() string {
	return "user.registered"
}

type OrderPlaced struct {
	OrderID int
	Amount  float64
}

func (e OrderPlaced) Name() string {
	return "order.placed"
}

// Test listener
type TestListener struct {
	handled   bool
	event     interface{}
	mu        sync.Mutex
	shouldErr bool
}

func (l *TestListener) Handle(event interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handled = true
	l.event = event
	if l.shouldErr {
		return errors.New("listener error")
	}
	return nil
}

func (l *TestListener) ShouldQueue() bool {
	return false
}

func (l *TestListener) WasHandled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handled
}

func (l *TestListener) GetEvent() interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.event
}

// CountingListener counts how many times it was called
type CountingListener struct {
	counter *int32
}

func (l *CountingListener) Handle(event interface{}) error {
	atomic.AddInt32(l.counter, 1)
	return nil
}

func (l *CountingListener) ShouldQueue() bool {
	return false
}

// Test subscriber
type TestSubscriber struct {
	listeners map[string]*TestListener
}

func NewTestSubscriber() *TestSubscriber {
	return &TestSubscriber{
		listeners: make(map[string]*TestListener),
	}
}

func (s *TestSubscriber) Subscribe(dispatcher Dispatcher) {
	s.listeners["user.registered"] = &TestListener{}
	s.listeners["order.placed"] = &TestListener{}

	dispatcher.Listen("user.registered", s.listeners["user.registered"])
	dispatcher.Listen("order.placed", s.listeners["order.placed"])
}

func TestBasicEventDispatching(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	// Register listener
	dispatcher.Listen("user.registered", listener)

	// Dispatch event
	event := UserRegistered{UserID: 1, Email: "test@example.com"}
	err := dispatcher.Dispatch(event)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !listener.WasHandled() {
		t.Error("Expected listener to handle event")
	}

	if listener.GetEvent() != event {
		t.Error("Expected listener to receive correct event")
	}
}

func TestMultipleListeners(t *testing.T) {
	dispatcher := NewDispatcher()
	listener1 := &TestListener{}
	listener2 := &TestListener{}

	// Register multiple listeners for same event
	dispatcher.Listen("user.registered", listener1)
	dispatcher.Listen("user.registered", listener2)

	// Dispatch event
	event := UserRegistered{UserID: 1, Email: "test@example.com"}
	err := dispatcher.Dispatch(event)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !listener1.WasHandled() || !listener2.WasHandled() {
		t.Error("Expected both listeners to handle event")
	}
}

func TestEventSubscriber(t *testing.T) {
	dispatcher := NewDispatcher()
	subscriber := NewTestSubscriber()

	// Register subscriber
	dispatcher.Subscribe(subscriber)

	// Dispatch events
	userEvent := UserRegistered{UserID: 1, Email: "test@example.com"}
	orderEvent := OrderPlaced{OrderID: 100, Amount: 99.99}

	dispatcher.Dispatch(userEvent)
	dispatcher.Dispatch(orderEvent)

	if !subscriber.listeners["user.registered"].WasHandled() {
		t.Error("Expected user.registered listener to handle event")
	}

	if !subscriber.listeners["order.placed"].WasHandled() {
		t.Error("Expected order.placed listener to handle event")
	}
}

func TestWildcardListeners(t *testing.T) {
	dispatcher := NewDispatcher()
	wildcardListener := &TestListener{}
	specificListener := &TestListener{}

	// Register wildcard and specific listeners
	dispatcher.Listen("user.*", wildcardListener)
	dispatcher.Listen("user.registered", specificListener)

	// Dispatch event
	event := UserRegistered{UserID: 1, Email: "test@example.com"}
	dispatcher.Dispatch(event)

	if !wildcardListener.WasHandled() {
		t.Error("Expected wildcard listener to handle event")
	}

	if !specificListener.WasHandled() {
		t.Error("Expected specific listener to handle event")
	}
}

func TestHasListeners(t *testing.T) {
	dispatcher := NewDispatcher()

	// Check no listeners initially
	if dispatcher.HasListeners("user.registered") {
		t.Error("Expected no listeners initially")
	}

	// Add listener
	dispatcher.Listen("user.registered", &TestListener{})

	// Check has listeners
	if !dispatcher.HasListeners("user.registered") {
		t.Error("Expected to have listeners")
	}
}

func TestForgetListeners(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	// Register listener
	dispatcher.Listen("user.registered", listener)

	// Forget listeners
	dispatcher.Forget("user.registered")

	// Check no listeners
	if dispatcher.HasListeners("user.registered") {
		t.Error("Expected no listeners after forget")
	}

	// Dispatch should not trigger listener
	event := UserRegistered{UserID: 1, Email: "test@example.com"}
	dispatcher.Dispatch(event)

	if listener.WasHandled() {
		t.Error("Expected listener not to handle event after forget")
	}
}

func TestConcurrentDispatching(t *testing.T) {
	dispatcher := NewDispatcher()
	var counter int32

	// Create custom listener that increments counter
	listener := &CountingListener{
		counter: &counter,
	}

	dispatcher.Listen("test.event", listener)

	// Dispatch events concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dispatcher.Dispatch("test.event")
		}()
	}

	wg.Wait()

	if atomic.LoadInt32(&counter) != 100 {
		t.Errorf("Expected counter to be 100, got %d", counter)
	}
}

func TestListenerError(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{shouldErr: true}

	dispatcher.Listen("test.event", listener)

	err := dispatcher.Dispatch("test.event")
	if err == nil {
		t.Error("Expected error from listener")
	}
}

func TestDispatchAsync(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	dispatcher.Listen("test.event", listener)

	// Dispatch async (will use goroutine since no queue configured)
	err := dispatcher.DispatchAsync("test.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Wait a bit for async execution
	time.Sleep(100 * time.Millisecond)

	if !listener.WasHandled() {
		t.Error("Expected listener to handle async event")
	}
}

func TestDispatchAfter(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	dispatcher.Listen("test.event", listener)

	start := time.Now()
	dispatcher.DispatchAfter("test.event", 100*time.Millisecond)

	// Wait for delayed execution
	time.Sleep(200 * time.Millisecond)

	if !listener.WasHandled() {
		t.Error("Expected listener to handle delayed event")
	}

	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Error("Event dispatched too early")
	}
}

func TestGlobalDispatcher(t *testing.T) {
	// Reset global dispatcher
	Reset()

	listener := &TestListener{}

	// Use global functions
	Listen("test.event", listener)

	if !HasListeners("test.event") {
		t.Error("Expected global dispatcher to have listeners")
	}

	Dispatch("test.event")

	if !listener.WasHandled() {
		t.Error("Expected global listener to handle event")
	}

	// Clean up
	Forget("test.event")

	if HasListeners("test.event") {
		t.Error("Expected no listeners after forget")
	}
}

func TestFakeDispatcher(t *testing.T) {
	// Set up fake dispatcher
	fake := Fake()

	// Register listener (won't actually execute)
	Listen("user.registered", &TestListener{})

	// Dispatch events
	event1 := UserRegistered{UserID: 1, Email: "test1@example.com"}
	event2 := UserRegistered{UserID: 2, Email: "test2@example.com"}
	event3 := OrderPlaced{OrderID: 100, Amount: 99.99}

	Dispatch(event1)
	Dispatch(event2)
	Dispatch(event3)

	// Assert events were dispatched
	err := fake.AssertDispatched(&UserRegistered{}, func(e interface{}) bool {
		event := e.(UserRegistered)
		return event.UserID == 1
	})
	if err != nil {
		t.Error(err)
	}

	// Assert dispatch count
	err = fake.AssertDispatchedTimes(&UserRegistered{}, 2)
	if err != nil {
		t.Error(err)
	}

	// Assert different event type
	err = fake.AssertDispatchedTimes(&OrderPlaced{}, 1)
	if err != nil {
		t.Error(err)
	}

	// Clear and assert nothing dispatched
	fake.ClearEvents()
	err = fake.AssertNothingDispatched()
	if err != nil {
		t.Error(err)
	}

	// Reset global dispatcher
	Reset()
}

func TestListenerIDReturned(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	// Listen should return a non-zero ID
	id := dispatcher.Listen("test.event", listener)
	if id == 0 {
		t.Error("Expected non-zero listener ID")
	}

	// Each listener should get a unique ID
	id2 := dispatcher.Listen("test.event", listener)
	if id2 == id {
		t.Error("Expected unique listener IDs")
	}
	if id2 <= id {
		t.Error("Expected IDs to be incrementing")
	}
}

func TestOffRemovesListener(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	// Register listener
	id := dispatcher.Listen("test.event", listener)

	// Verify listener is registered
	if !dispatcher.HasListeners("test.event") {
		t.Error("Expected listener to be registered")
	}

	// Remove listener by ID
	removed := dispatcher.Off(id)
	if !removed {
		t.Error("Expected Off to return true")
	}

	// Verify listener is removed
	if dispatcher.HasListeners("test.event") {
		t.Error("Expected listener to be removed")
	}

	// Dispatch should not trigger listener
	dispatcher.Dispatch("test.event")
	if listener.WasHandled() {
		t.Error("Expected listener not to be called after Off")
	}
}

func TestOffWithInvalidID(t *testing.T) {
	dispatcher := NewDispatcher()

	// Off with non-existent ID should return false
	removed := dispatcher.Off(9999)
	if removed {
		t.Error("Expected Off to return false for invalid ID")
	}
}

func TestOffRemovesOnlySpecificListener(t *testing.T) {
	dispatcher := NewDispatcher()
	listener1 := &TestListener{}
	listener2 := &TestListener{}

	// Register two listeners
	id1 := dispatcher.Listen("test.event", listener1)
	_ = dispatcher.Listen("test.event", listener2)

	// Remove first listener
	dispatcher.Off(id1)

	// Dispatch event
	dispatcher.Dispatch("test.event")

	// Only listener2 should be called
	if listener1.WasHandled() {
		t.Error("Expected listener1 not to be called")
	}
	if !listener2.WasHandled() {
		t.Error("Expected listener2 to be called")
	}
}

func TestOffWithWildcardListener(t *testing.T) {
	dispatcher := NewDispatcher()
	listener := &TestListener{}

	// Register wildcard listener
	id := dispatcher.Listen("user.*", listener)

	// Verify it matches
	if !dispatcher.HasListeners(UserRegistered{}) {
		t.Error("Expected wildcard listener to match")
	}

	// Remove it
	removed := dispatcher.Off(id)
	if !removed {
		t.Error("Expected Off to return true for wildcard listener")
	}

	// Verify it's removed
	if dispatcher.HasListeners(UserRegistered{}) {
		t.Error("Expected wildcard listener to be removed")
	}
}

func TestGlobalOnReturnsID(t *testing.T) {
	Reset()

	handlerCalled := false
	id := On("test.event", func(e interface{}) error {
		handlerCalled = true
		return nil
	})

	if id == 0 {
		t.Error("Expected non-zero listener ID from On()")
	}

	// Verify it works
	Dispatch("test.event")
	if !handlerCalled {
		t.Error("Expected handler to be called")
	}

	// Remove and verify
	handlerCalled = false
	Off(id)
	Dispatch("test.event")
	if handlerCalled {
		t.Error("Expected handler not to be called after Off")
	}

	Reset()
}

func TestConcurrentOnOff(t *testing.T) {
	dispatcher := NewDispatcher()
	var wg sync.WaitGroup

	// Concurrently add and remove listeners
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			listener := &TestListener{}
			id := dispatcher.Listen("concurrent.event", listener)
			// Small delay to mix operations
			dispatcher.Off(id)
		}()
	}

	wg.Wait()

	// All listeners should be removed
	if dispatcher.HasListeners("concurrent.event") {
		t.Error("Expected all listeners to be removed")
	}
}
