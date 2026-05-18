package events

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// Test helper types for complete coverage
type SimpleEvent struct {
	EventName string
}

func (e *SimpleEvent) Name() string {
	return e.EventName
}

type SimpleListener struct {
	HandleFunc func(ctx context.Context, event interface{}) error
}

func (l *SimpleListener) Handle(ctx context.Context, event interface{}) error {
	if l.HandleFunc != nil {
		return l.HandleFunc(ctx, event)
	}
	return nil
}

func (l *SimpleListener) ShouldQueue() bool {
	return false
}

type SimpleQueuedListener struct {
	HandleFunc     func(ctx context.Context, event interface{}) error
	ConnectionName string
	QueueName      string
	DelayDuration  time.Duration
	MaxTries       int
}

func (l *SimpleQueuedListener) Handle(ctx context.Context, event interface{}) error {
	if l.HandleFunc != nil {
		return l.HandleFunc(ctx, event)
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
	err := listener.Handle(context.Background(), "any event")
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
	err := dispatcher.Push(context.Background(), "test", listener, 10*time.Millisecond)
	if err != nil {
		t.Errorf("Push failed: %v", err)
	}

	testsync.Eventually(t, listener.WasHandled, time.Second, "async listener handles event")

	// Test Push with error — just verify it doesn't leak the error up.
	errorListener := &TestListener{shouldErr: true}
	if err := dispatcher.Push(context.Background(), "error", errorListener, 0); err != nil {
		t.Error("Push should not return listener errors")
	}
	testsync.Eventually(t, errorListener.WasHandled, time.Second, "error listener handled")
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
			HandleFunc: func(ctx context.Context, event interface{}) error {
				return nil
			},
		}
	}

	worker.RegisterListener("test", factory)

	// Process with proper JSON
	jobJSON := `{"listener_type":"test","event":{"data":"test"}}`
	err := worker.Process(context.Background(), jobJSON)
	if err != nil {
		t.Errorf("Process failed: %v", err)
	}

	if counter != 1 {
		t.Errorf("Factory should be called once, got %d", counter)
	}

	// Test with invalid JSON
	err = worker.Process(context.Background(), "invalid json")
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}

	// Test with unknown listener type
	err = worker.Process(context.Background(), `{"listener_type":"unknown","event":{}}`)
	if err == nil {
		t.Error("Expected error for unknown listener type")
	}

	// Test with listener that returns error
	errorFactory := func() Listener {
		return &TestListener{shouldErr: true}
	}
	worker.RegisterListener("error", errorFactory)

	err = worker.Process(context.Background(), `{"listener_type":"error","event":{}}`)
	if err == nil {
		t.Error("Expected error from listener Handle")
	}
}

// TestEventWorker_ConcurrentRegisterAndProcess hammers RegisterListener and
// Process from separate goroutines to confirm the listeners map is properly
// guarded. With -race this would fail (or trigger a fatal "concurrent map
// read and map write" runtime panic) before the fix that added sync.RWMutex
// protection.
func TestEventWorker_ConcurrentRegisterAndProcess(t *testing.T) {
	worker := NewEventWorker(NewDispatcher())

	factory := func() Listener {
		return &SimpleListener{
			HandleFunc: func(ctx context.Context, event interface{}) error {
				return nil
			},
		}
	}
	// Seed at least one entry so Process has something to find.
	worker.RegisterListener("seed", factory)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: keep registering new listener types.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				worker.RegisterListener("type-"+strings.Repeat("x", i%8), factory)
				i++
			}
		}
	}()

	// Reader: keep processing jobs (hits the map read path).
	wg.Add(1)
	go func() {
		defer wg.Done()
		jobJSON := `{"listener_type":"seed","event":{"data":"x"}}`
		for {
			select {
			case <-stop:
				return
			default:
				_ = worker.Process(context.Background(), jobJSON)
			}
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// Test Dispatcher with queue error
func TestDispatcherQueueError(t *testing.T) {
	d := NewDispatcher()
	errorQueue := &testQueueError{}
	d.SetQueueDispatcher(errorQueue)

	queuedListener := &TestQueuedListener{}
	d.Listen("queued", queuedListener)

	// Dispatch should return error when queue Push fails
	err := d.Dispatch(context.Background(), "queued")
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
	err := d.processListener(context.Background(), "event", listener)
	if err != nil {
		t.Errorf("Expected nil when ShouldHandle is false, got %v", err)
	}

	// Test with ShouldHandle returning true
	listener.shouldHandle = true
	err = d.processListener(context.Background(), "event", listener)
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
	fake.Dispatch(context.Background(), "event1")
	fake.Dispatch(context.Background(), "event2")
	fake.Dispatch(context.Background(), "event3")

	if len(fake.GetDispatchedEvents()) != 3 {
		t.Errorf("Expected 3 events, got %d", len(fake.GetDispatchedEvents()))
	}

	// Test with Event interface
	event := &UserRegistered{UserID: 1}
	fake.Listen(event, listener)
	fake.Dispatch(context.Background(), event)

	// Test StartFaking and StopFaking
	fake.StopFaking()
	handled := false
	testListener := &SimpleListener{
		HandleFunc: func(ctx context.Context, event interface{}) error {
			handled = true
			return nil
		},
	}
	fake.Listen("real", testListener)
	fake.Dispatch(context.Background(), "real")

	if !handled {
		t.Error("Listener should be executed when not faking")
	}

	fake.StartFaking()
	handled = false
	fake.Dispatch(context.Background(), "real2")

	if handled {
		t.Error("Listener should not be executed when faking")
	}

	// Test executeListeners with error
	fake.StopFaking()
	errorListener := &TestListener{shouldErr: true}
	fake.Listen("error.event", errorListener)
	fake.executeListeners(context.Background(), "error.event")
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
	fake.Dispatch(context.Background(), "test.event")
	fake.Dispatch(context.Background(), UserRegistered{UserID: 1})

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
	fake3.Dispatch(context.Background(), "string.event")
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
	if err := txDispatcher.DispatchAfterCommit(context.Background(), "tx"); err != nil {
		t.Fatalf("DispatchAfterCommit in tx returned error: %v", err)
	}

	// Should not be handled yet
	if listener.WasHandled() {
		t.Error("Should not be handled before commit")
	}

	// Commit
	txDispatcher.Commit(context.Background())

	// Should be handled now
	if !listener.WasHandled() {
		t.Error("Should be handled after commit")
	}

	// Test rollback
	listener2 := &TestListener{}
	baseDispatcher.Listen("rollback", listener2)

	txDispatcher.BeginTransaction()
	if err := txDispatcher.DispatchAfterCommit(context.Background(), "rollback"); err != nil {
		t.Fatalf("DispatchAfterCommit in tx returned error: %v", err)
	}
	txDispatcher.Rollback()

	if listener2.WasHandled() {
		t.Error("Should not be handled after rollback")
	}

	// Test without transaction
	listener3 := &TestListener{}
	baseDispatcher.Listen("direct", listener3)

	if err := txDispatcher.DispatchAfterCommit(context.Background(), "direct"); err != nil {
		t.Fatalf("DispatchAfterCommit without tx returned error: %v", err)
	}

	if !listener3.WasHandled() {
		t.Error("Should be handled immediately without transaction")
	}

	// Test commit with error
	baseDispatcher.Listen("error", &TestListener{shouldErr: true})
	txDispatcher.BeginTransaction()
	if err := txDispatcher.DispatchAfterCommit(context.Background(), "error"); err != nil {
		t.Fatalf("DispatchAfterCommit in tx returned error: %v", err)
	}
	txDispatcher.Commit(context.Background()) // Should handle error gracefully
}

// TestDispatchAfterCommit_NonTxBranchSurfacesError pins the contract that
// DispatchAfterCommit returns the dispatcher's error when it falls through
// to direct dispatch (no active transaction). The earlier signature
// returned nothing, which silently swallowed listener errors outside a tx.
func TestDispatchAfterCommit_NonTxBranchSurfacesError(t *testing.T) {
	baseDispatcher := NewDispatcher()
	txDispatcher := NewTransactionalDispatcher(baseDispatcher)

	// Listener returns an error so Dispatch surfaces a non-nil err.
	baseDispatcher.Listen("boom", &TestListener{shouldErr: true})

	// No active transaction -> falls through to direct dispatch.
	err := txDispatcher.DispatchAfterCommit(context.Background(), "boom")
	if err == nil {
		t.Fatal("expected DispatchAfterCommit to surface dispatcher error outside a tx")
	}
}

// TestDispatchAfterCommit_TxBranchReturnsNil verifies the in-tx branch
// always returns nil (events are queued, not dispatched).
func TestDispatchAfterCommit_TxBranchReturnsNil(t *testing.T) {
	baseDispatcher := NewDispatcher()
	txDispatcher := NewTransactionalDispatcher(baseDispatcher)
	baseDispatcher.Listen("queued", &TestListener{shouldErr: true})

	txDispatcher.BeginTransaction()
	defer txDispatcher.Rollback()

	if err := txDispatcher.DispatchAfterCommit(context.Background(), "queued"); err != nil {
		t.Fatalf("expected nil from in-tx DispatchAfterCommit, got %v", err)
	}
}

// Test dispatcher error paths
func TestDispatcherErrorPaths(t *testing.T) {
	d := NewDispatcher()

	// Add multiple listeners, some with errors
	d.Listen("mixed", &TestListener{})
	d.Listen("mixed", &TestListener{shouldErr: true})
	d.Listen("mixed", &TestListener{})

	// Should return first error encountered
	err := d.Dispatch(context.Background(), "mixed")
	if err == nil {
		t.Error("Expected error from failing listener")
	}

	// Test DispatchNow with error
	err = d.DispatchNow(context.Background(), "mixed")
	if err == nil {
		t.Error("Expected error from DispatchNow")
	}

	// Test Until with error
	_, err = d.Until(context.Background(), "mixed")
	if err == nil {
		t.Error("Expected error from Until")
	}

	// Test Until with HandleWithResult
	listener := &resultListener{result: "test-result"}
	d.Listen("result", listener)

	result, err := d.Until(context.Background(), "result")
	if err != nil {
		t.Errorf("Until failed: %v", err)
	}

	if result != "test-result" {
		t.Errorf("Expected test-result, got %v", result)
	}

	// Test Until with HandleWithResult error
	errorListener := &resultListener{resultErr: errors.New("result error")}
	d.Listen("result.error", errorListener)

	_, err = d.Until(context.Background(), "result.error")
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
	// "test" is not valid job JSON so Process errors out — this is purely a
	// smoke test that the call doesn't panic when given garbage input.
	_ = bus.ProcessQueuedEvent(context.Background(), "test")
}

// Test FakeDispatcher uncovered methods
func TestFakeDispatcherUncoveredMethods(t *testing.T) {
	fake := NewFakeDispatcher()

	// Test DispatchNow
	err := fake.DispatchNow(context.Background(), "test.event")
	if err != nil {
		t.Errorf("DispatchNow failed: %v", err)
	}

	// Test DispatchAsync
	err = fake.DispatchAsync(context.Background(), "async.event")
	if err != nil {
		t.Errorf("DispatchAsync failed: %v", err)
	}

	// Test DispatchAfter
	err = fake.DispatchAfter(context.Background(), "delayed.event", 10*time.Millisecond)
	if err != nil {
		t.Errorf("DispatchAfter failed: %v", err)
	}

	// Test Until
	result, err := fake.Until(context.Background(), "until.event")
	if err != nil {
		t.Errorf("Until failed: %v", err)
	}
	if result != nil {
		t.Error("Until should return nil for fake")
	}
}

// Test dispatcher DispatchNow and Until
func TestDispatcherDispatchNowAndUntil(t *testing.T) {
	d := NewDispatcher()

	// Test DispatchNow
	err := d.DispatchNow(context.Background(), "test.event")
	if err != nil {
		t.Errorf("DispatchNow failed: %v", err)
	}

	// Test Until
	result, err := d.Until(context.Background(), "test.event")
	if err != nil {
		t.Errorf("Until failed: %v", err)
	}
	if result != nil {
		t.Error("Until should return nil by default")
	}
}

// Test Listen with wildcard pattern
func TestListenWildcard(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	// Test with wildcard pattern
	d.Listen("user.*", listener)

	// Dispatch should match
	d.Dispatch(context.Background(), "user.created")
	d.Dispatch(context.Background(), "user.updated")
}

// Test DispatchAsync and DispatchAfter without queue
func TestDispatchAsyncAfterWithoutQueue(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}
	d.Listen("test", listener)

	// DispatchAsync without queue (should use goroutine)
	if err := d.DispatchAsync(context.Background(), "test"); err != nil {
		t.Errorf("DispatchAsync failed: %v", err)
	}

	testsync.Eventually(t, listener.WasHandled, time.Second, "async goroutine handles event")

	// DispatchAfter without queue
	listener2 := &TestListener{}
	d.Listen("delayed", listener2)

	if err := d.DispatchAfter(context.Background(), "delayed", 10*time.Millisecond); err != nil {
		t.Errorf("DispatchAfter failed: %v", err)
	}

	testsync.Eventually(t, listener2.WasHandled, time.Second, "delayed listener handles event")
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
	mu      sync.Mutex
	handled bool
}

func (l *TestFullQueuedListener) Handle(ctx context.Context, event interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handled = true
	return nil
}

func (l *TestFullQueuedListener) WasHandled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handled
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

func (q *testQueueError) Push(ctx context.Context, event interface{}, listener Listener, delay time.Duration) error {
	return errors.New("queue push failed")
}

type shouldHandleListener struct {
	shouldHandle bool
	handled      bool
}

func (l *shouldHandleListener) Handle(ctx context.Context, event interface{}) error {
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

func (l *resultListener) Handle(ctx context.Context, event interface{}) error {
	return nil
}

func (l *resultListener) ShouldQueue() bool {
	return false
}

func (l *resultListener) HandleWithResult(ctx context.Context, event interface{}) (interface{}, error) {
	return l.result, l.resultErr
}
