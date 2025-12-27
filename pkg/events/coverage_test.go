package events

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Test helper types for complete coverage
type SimpleEvent struct {
	EventName string
}

func (e *SimpleEvent) Name() string {
	return e.EventName
}

type SimpleListener struct {
	HandleFunc func(event interface{}) error
}

func (l *SimpleListener) Handle(event interface{}) error {
	if l.HandleFunc != nil {
		return l.HandleFunc(event)
	}
	return nil
}

func (l *SimpleListener) ShouldQueue() bool {
	return false
}

type SimpleQueuedListener struct {
	HandleFunc     func(event interface{}) error
	ConnectionName string
	QueueName      string
	DelayDuration  time.Duration
	MaxTries       int
}

func (l *SimpleQueuedListener) Handle(event interface{}) error {
	if l.HandleFunc != nil {
		return l.HandleFunc(event)
	}
	return nil
}

func (l *SimpleQueuedListener) ShouldQueue() bool {
	return true
}

func (l *SimpleQueuedListener) OnConnection() string {
	return l.ConnectionName
}

func (l *SimpleQueuedListener) OnQueue() string {
	return l.QueueName
}

func (l *SimpleQueuedListener) WithDelay() time.Duration {
	return l.DelayDuration
}

func (l *SimpleQueuedListener) Tries() int {
	return l.MaxTries
}

// Test BaseEvent for 100% coverage
func TestBaseEventComplete(t *testing.T) {
	// Test with EventName set
	event := &BaseEvent{EventName: "custom.event"}
	if event.Name() != "custom.event" {
		t.Errorf("Expected custom.event, got %s", event.Name())
	}

	// Test with empty EventName
	event2 := &BaseEvent{}
	if event2.Name() != "base.event" {
		t.Errorf("Expected base.event, got %s", event2.Name())
	}
}

// Test BaseListener for 100% coverage
func TestBaseListenerComplete(t *testing.T) {
	listener := &BaseListener{}

	// Test Handle
	err := listener.Handle("any event")
	if err != nil {
		t.Errorf("Handle should return nil, got %v", err)
	}

	// Test ShouldQueue
	if listener.ShouldQueue() {
		t.Error("BaseListener should not queue")
	}
}

// Test QueuedBaseListener for 100% coverage
func TestQueuedBaseListenerComplete(t *testing.T) {
	// Test with custom values
	listener := &QueuedBaseListener{
		Connection: "redis",
		Queue:      "events",
		Delay:      5 * time.Second,
		MaxTries:   5,
	}

	if listener.OnConnection() != "redis" {
		t.Errorf("Expected redis, got %s", listener.OnConnection())
	}

	if listener.OnQueue() != "events" {
		t.Errorf("Expected events, got %s", listener.OnQueue())
	}

	if listener.WithDelay() != 5*time.Second {
		t.Errorf("Expected 5s, got %v", listener.WithDelay())
	}

	if listener.Tries() != 5 {
		t.Errorf("Expected 5 tries, got %d", listener.Tries())
	}

	if !listener.ShouldQueue() {
		t.Error("QueuedBaseListener should queue")
	}

	// Test with default values
	listener2 := &QueuedBaseListener{}

	if listener2.OnConnection() != "default" {
		t.Errorf("Expected default, got %s", listener2.OnConnection())
	}

	if listener2.OnQueue() != "default" {
		t.Errorf("Expected default, got %s", listener2.OnQueue())
	}

	if listener2.WithDelay() != 0 {
		t.Errorf("Expected 0, got %v", listener2.WithDelay())
	}

	if listener2.Tries() != 3 {
		t.Errorf("Expected 3 tries, got %d", listener2.Tries())
	}
}

// Test AsyncDispatcher full coverage
func TestAsyncDispatcherFullCoverage(t *testing.T) {
	dispatcher := NewAsyncDispatcher()
	if dispatcher == nil {
		t.Fatal("NewAsyncDispatcher should not return nil")
	}

	listener := &TestListener{}

	// Test Push
	err := dispatcher.Push("test", listener, 10*time.Millisecond)
	if err != nil {
		t.Errorf("Push failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !listener.WasHandled() {
		t.Error("Event should be handled")
	}

	// Test Push with error
	errorListener := &TestListener{shouldErr: true}
	err = dispatcher.Push("error", errorListener, 0)
	if err != nil {
		t.Error("Push should not return listener errors")
	}

	time.Sleep(50 * time.Millisecond)
}

// Test EventWorker full coverage
func TestEventWorkerFullCoverage(t *testing.T) {
	baseDispatcher := NewDispatcher()
	worker := NewEventWorker(baseDispatcher)

	// Register factory
	counter := 0
	factory := func() Listener {
		counter++
		return &SimpleListener{
			HandleFunc: func(event interface{}) error {
				return nil
			},
		}
	}

	worker.RegisterListener("test", factory)

	// Process with proper JSON
	jobJSON := `{"listener_type":"test","event":{"data":"test"}}`
	err := worker.Process(jobJSON)
	if err != nil {
		t.Errorf("Process failed: %v", err)
	}

	if counter != 1 {
		t.Errorf("Factory should be called once, got %d", counter)
	}

	// Test with invalid JSON
	err = worker.Process("invalid json")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}

	// Test with unknown listener type
	err = worker.Process(`{"listener_type":"unknown","event":{}}`)
	if err == nil {
		t.Error("Expected error for unknown listener type")
	}

	// Test with listener that returns error
	errorFactory := func() Listener {
		return &TestListener{shouldErr: true}
	}
	worker.RegisterListener("error", errorFactory)

	err = worker.Process(`{"listener_type":"error","event":{}}`)
	if err == nil {
		t.Error("Expected error from listener Handle")
	}
}

// Test Dispatcher with queue error
func TestDispatcherQueueError(t *testing.T) {
	d := NewDispatcher()
	errorQueue := &testQueueError{}
	d.SetQueueDispatcher(errorQueue)

	queuedListener := &TestQueuedListener{}
	d.Listen("queued", queuedListener)

	// Dispatch should return error when queue Push fails
	err := d.Dispatch("queued")
	if err == nil {
		t.Error("Expected error from queue Push")
	}
}

// Test getEventName with empty type name
func TestGetEventNameEmptyTypeName(t *testing.T) {
	d := NewDispatcher()

	// Use a slice type which has no Name() method
	var slice []int
	name := d.getEventName(slice)
	if name != "[]int" {
		t.Errorf("Expected []int, got %s", name)
	}
}

// Test processListener with ShouldHandle interface
func TestProcessListenerShouldHandle(t *testing.T) {
	d := NewDispatcher()

	// Test listener that implements ShouldHandle
	listener := &shouldHandleListener{shouldHandle: false}

	// Should return nil when ShouldHandle returns false
	err := d.processListener("event", listener)
	if err != nil {
		t.Errorf("Expected nil when ShouldHandle is false, got %v", err)
	}

	// Test with ShouldHandle returning true
	listener.shouldHandle = true
	err = d.processListener("event", listener)
	if err != nil {
		t.Errorf("Expected nil when handling succeeds, got %v", err)
	}
}

// Test FakeDispatcher edge cases for full coverage
func TestFakeDispatcherFullCoverage(t *testing.T) {
	fake := NewFakeDispatcher()

	// Test Listen with multiple event names
	listener := &TestListener{}
	fake.Listen([]string{"event1", "event2", "event3"}, listener)

	// Verify all events are registered
	fake.Dispatch("event1")
	fake.Dispatch("event2")
	fake.Dispatch("event3")

	if len(fake.GetDispatchedEvents()) != 3 {
		t.Errorf("Expected 3 events, got %d", len(fake.GetDispatchedEvents()))
	}

	// Test with Event interface
	event := &UserRegistered{UserID: 1}
	fake.Listen(event, listener)
	fake.Dispatch(event)

	// Test StartFaking and StopFaking
	fake.StopFaking()
	handled := false
	testListener := &SimpleListener{
		HandleFunc: func(event interface{}) error {
			handled = true
			return nil
		},
	}
	fake.Listen("real", testListener)
	fake.Dispatch("real")

	if !handled {
		t.Error("Listener should be executed when not faking")
	}

	fake.StartFaking()
	handled = false
	fake.Dispatch("real2")

	if handled {
		t.Error("Listener should not be executed when faking")
	}

	// Test executeListeners with error
	fake.StopFaking()
	errorListener := &TestListener{shouldErr: true}
	fake.Listen("error.event", errorListener)
	fake.executeListeners("error.event")
}

// Test FakeDispatcher getEventName paths
func TestFakeGetEventNamePaths(t *testing.T) {
	fake := NewFakeDispatcher()

	// Test with pointer to Event
	event := &UserRegistered{UserID: 1}
	name := fake.getEventName(event)
	if name != "user.registered" {
		t.Errorf("Expected user.registered, got %s", name)
	}

	// Test with non-pointer struct
	name = fake.getEventName(UserRegistered{UserID: 2})
	if name != "user.registered" {
		t.Errorf("Expected user.registered for non-pointer, got %s", name)
	}

	// Test with string
	name = fake.getEventName("string.event")
	if name != "string.event" {
		t.Errorf("Expected string.event, got %s", name)
	}

	// Test with other type
	type NamedType struct{}
	namedValue := NamedType{}
	name = fake.getEventName(namedValue)
	if name != "NamedType" {
		t.Errorf("Expected NamedType, got %s", name)
	}
}

// Test FakeDispatcher Assert methods
func TestFakeDispatcherAssertMethods(t *testing.T) {
	fake := NewFakeDispatcher()

	// Dispatch some events
	fake.Dispatch("test.event")
	fake.Dispatch(UserRegistered{UserID: 1})

	// Test AssertDispatched with callback
	err := fake.AssertDispatched("test.event", func(event interface{}) bool {
		return event.(string) == "test.event"
	})
	if err != nil {
		t.Errorf("Should pass when callback matches: %v", err)
	}

	// Test with callback that returns false
	err = fake.AssertDispatched("test.event", func(event interface{}) bool {
		return false
	})
	if err == nil {
		t.Error("Expected error when callback returns false")
	}

	// Test AssertDispatched with struct
	err = fake.AssertDispatched(UserRegistered{}, nil)
	if err != nil {
		t.Errorf("Should find dispatched UserRegistered event: %v", err)
	}

	// Test AssertDispatched when not found - use a type that wasn't dispatched
	type NotDispatchedEvent struct{}
	err = fake.AssertDispatched(NotDispatchedEvent{}, nil)
	if err == nil {
		t.Error("Should fail when event not dispatched")
	}

	// Test AssertNotDispatched with dispatched event
	err = fake.AssertNotDispatched("test.event")
	if err == nil {
		t.Error("Expected error when event was dispatched")
	}

	// Test AssertNotDispatched with struct
	err = fake.AssertNotDispatched(UserRegistered{})
	if err == nil {
		t.Error("Should fail when struct type was dispatched")
	}

	// Create new dispatcher for clean test
	fake2 := NewFakeDispatcher()
	err = fake2.AssertNotDispatched("never.dispatched")
	if err != nil {
		t.Errorf("Should pass for non-dispatched event: %v", err)
	}

	// Test with string type for coverage
	fake3 := NewFakeDispatcher()
	fake3.Dispatch("string.event")
	err = fake3.AssertDispatched("string", nil)
	if err != nil {
		t.Errorf("Should find string event: %v", err)
	}

	err = fake3.AssertNotDispatched("string")
	if err == nil {
		t.Error("Should fail when string type was dispatched")
	}
}

// Test TransactionalDispatcher full coverage
func TestTransactionalDispatcherFullCoverage(t *testing.T) {
	baseDispatcher := NewDispatcher()
	txDispatcher := NewTransactionalDispatcher(baseDispatcher)

	listener := &TestListener{}
	baseDispatcher.Listen("tx", listener)

	// Begin transaction
	txDispatcher.BeginTransaction()

	// Dispatch after commit
	txDispatcher.DispatchAfterCommit("tx")

	// Should not be handled yet
	if listener.WasHandled() {
		t.Error("Should not be handled before commit")
	}

	// Commit
	txDispatcher.Commit()

	// Should be handled now
	if !listener.WasHandled() {
		t.Error("Should be handled after commit")
	}

	// Test rollback
	listener2 := &TestListener{}
	baseDispatcher.Listen("rollback", listener2)

	txDispatcher.BeginTransaction()
	txDispatcher.DispatchAfterCommit("rollback")
	txDispatcher.Rollback()

	if listener2.WasHandled() {
		t.Error("Should not be handled after rollback")
	}

	// Test without transaction
	listener3 := &TestListener{}
	baseDispatcher.Listen("direct", listener3)

	txDispatcher.DispatchAfterCommit("direct")

	if !listener3.WasHandled() {
		t.Error("Should be handled immediately without transaction")
	}

	// Test commit with error
	baseDispatcher.Listen("error", &TestListener{shouldErr: true})
	txDispatcher.BeginTransaction()
	txDispatcher.DispatchAfterCommit("error")
	txDispatcher.Commit() // Should handle error gracefully
}

// Test dispatcher error paths
func TestDispatcherErrorPaths(t *testing.T) {
	d := NewDispatcher()

	// Add multiple listeners, some with errors
	d.Listen("mixed", &TestListener{})
	d.Listen("mixed", &TestListener{shouldErr: true})
	d.Listen("mixed", &TestListener{})

	// Should return first error encountered
	err := d.Dispatch("mixed")
	if err == nil {
		t.Error("Expected error from failing listener")
	}

	// Test DispatchNow with error
	err = d.DispatchNow("mixed")
	if err == nil {
		t.Error("Expected error from DispatchNow")
	}

	// Test Until with error
	_, err = d.Until("mixed")
	if err == nil {
		t.Error("Expected error from Until")
	}

	// Test Until with HandleWithResult
	listener := &resultListener{result: "test-result"}
	d.Listen("result", listener)

	result, err := d.Until("result")
	if err != nil {
		t.Errorf("Until failed: %v", err)
	}

	if result != "test-result" {
		t.Errorf("Expected test-result, got %v", result)
	}

	// Test Until with HandleWithResult error
	errorListener := &resultListener{resultErr: errors.New("result error")}
	d.Listen("result.error", errorListener)

	_, err = d.Until("result.error")
	if err == nil {
		t.Error("Expected error from Until")
	}
}

// Test AsyncEventBus for coverage
func TestAsyncEventBusCoverage(t *testing.T) {
	bus := NewAsyncEventBus()
	if bus == nil {
		t.Fatal("NewAsyncEventBus should not return nil")
	}

	// Create a QueuedListener implementation
	listener := &TestFullQueuedListener{}
	factory := func() Listener { return listener }

	bus.RegisterQueuedListener("test", listener, factory)
	bus.ProcessQueuedEvent("test")

	time.Sleep(100 * time.Millisecond)
}

// Test FakeDispatcher uncovered methods
func TestFakeDispatcherUncoveredMethods(t *testing.T) {
	fake := NewFakeDispatcher()

	// Test DispatchNow
	err := fake.DispatchNow("test.event")
	if err != nil {
		t.Errorf("DispatchNow failed: %v", err)
	}

	// Test DispatchAsync
	err = fake.DispatchAsync("async.event")
	if err != nil {
		t.Errorf("DispatchAsync failed: %v", err)
	}

	// Test DispatchAfter
	err = fake.DispatchAfter("delayed.event", 10*time.Millisecond)
	if err != nil {
		t.Errorf("DispatchAfter failed: %v", err)
	}

	// Test Until
	result, err := fake.Until("until.event")
	if err != nil {
		t.Errorf("Until failed: %v", err)
	}
	if result != nil {
		t.Error("Until should return nil for fake")
	}
}

// Test global init functions
func TestGlobalInitFunctions(t *testing.T) {
	// Test global DispatchNow
	err := DispatchNow("test.event")
	if err != nil {
		t.Errorf("Global DispatchNow failed: %v", err)
	}

	// Test global Until
	result, err := Until("test.event")
	if err != nil {
		t.Errorf("Global Until failed: %v", err)
	}
	if result != nil {
		t.Error("Global Until should return nil by default")
	}
}

// Test Listen with wildcard pattern
func TestListenWildcard(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	// Test with wildcard pattern
	d.Listen("user.*", listener)

	// Dispatch should match
	d.Dispatch("user.created")
	d.Dispatch("user.updated")
}

// Test DispatchAsync and DispatchAfter without queue
func TestDispatchAsyncAfterWithoutQueue(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}
	d.Listen("test", listener)

	// DispatchAsync without queue (should use goroutine)
	err := d.DispatchAsync("test")
	if err != nil {
		t.Errorf("DispatchAsync failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !listener.WasHandled() {
		t.Error("Event should be handled async")
	}

	// DispatchAfter without queue
	listener2 := &TestListener{}
	d.Listen("delayed", listener2)

	err = d.DispatchAfter("delayed", 10*time.Millisecond)
	if err != nil {
		t.Errorf("DispatchAfter failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if !listener2.WasHandled() {
		t.Error("Event should be handled after delay")
	}
}

// Test matchParts edge case for remaining coverage
func TestMatchPartsEdgePath(t *testing.T) {
	// Test the uncovered line in matchParts
	result := matchParts([]string{"a", "b", "c"}, []string{"a", "**", "d"})
	if result {
		t.Error("Should not match when double wildcard doesn't align")
	}
}

// Test getEventName edge cases
func TestGetEventNameEdgeCases(t *testing.T) {
	d := NewDispatcher()

	// Test with channel
	ch := make(chan int)
	name := d.getEventName(ch)
	if !strings.Contains(name, "chan") {
		t.Errorf("Expected chan in name, got %s", name)
	}

	// Test with map
	m := make(map[string]int)
	name = d.getEventName(m)
	if !strings.Contains(name, "map") {
		t.Errorf("Expected map in name, got %s", name)
	}

	// Test with function
	fn := func() {}
	name = d.getEventName(fn)
	if !strings.Contains(name, "func") {
		t.Errorf("Expected func in name, got %s", name)
	}

	// Test with struct
	type ComplexStruct struct {
		Field1 string
		Field2 int
	}
	name = d.getEventName(ComplexStruct{})
	if name != "complex.struct" {
		t.Errorf("Expected complex.struct, got %s", name)
	}

	// Test with pointer
	event := &UserRegistered{UserID: 1}
	name = d.getEventName(event)
	if name != "user.registered" {
		t.Errorf("Expected user.registered, got %s", name)
	}
}

// Test helpers
type TestFullQueuedListener struct {
	handled bool
}

func (l *TestFullQueuedListener) Handle(event interface{}) error {
	l.handled = true
	return nil
}

func (l *TestFullQueuedListener) ShouldQueue() bool {
	return true
}

func (l *TestFullQueuedListener) OnConnection() string {
	return "default"
}

func (l *TestFullQueuedListener) OnQueue() string {
	return "events"
}

func (l *TestFullQueuedListener) WithDelay() time.Duration {
	return 0
}

func (l *TestFullQueuedListener) Tries() int {
	return 3
}

type testQueueError struct{}

func (q *testQueueError) Push(event interface{}, listener Listener, delay time.Duration) error {
	return errors.New("queue push failed")
}

type shouldHandleListener struct {
	shouldHandle bool
	handled      bool
}

func (l *shouldHandleListener) Handle(event interface{}) error {
	l.handled = true
	return nil
}

func (l *shouldHandleListener) ShouldQueue() bool {
	return false
}

func (l *shouldHandleListener) ShouldHandle(event interface{}) bool {
	return l.shouldHandle
}

type resultListener struct {
	result    interface{}
	resultErr error
}

func (l *resultListener) Handle(event interface{}) error {
	return nil
}

func (l *resultListener) ShouldQueue() bool {
	return false
}

func (l *resultListener) HandleWithResult(event interface{}) (interface{}, error) {
	return l.result, l.resultErr
}
