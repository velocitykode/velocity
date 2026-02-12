package events

import (
	"testing"
	"time"
)

// Mock queue dispatcher for testing
type mockQueueDispatcher struct {
	pushed []interface{}
}

func (m *mockQueueDispatcher) Push(event interface{}, listener Listener, delay time.Duration) error {
	m.pushed = append(m.pushed, event)
	// Simulate async execution
	go func() {
		time.Sleep(delay)
		listener.Handle(event)
	}()
	return nil
}

func TestCamelToSnake(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"UserRegistered", "user.registered"},
		{"OrderPlaced", "order.placed"},
		{"PaymentProcessed", "payment.processed"},
		{"APIKeyGenerated", "a.p.i.key.generated"},
		{"HTTPSConnectionEstablished", "h.t.t.p.s.connection.established"},
		{"simpleEvent", "simple.event"},
		{"Event", "event"},
		{"", ""},
	}

	for _, test := range tests {
		result := camelToSnake(test.input)
		if result != test.expected {
			t.Errorf("camelToSnake(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

func TestDispatcherUntil(t *testing.T) {
	d := NewDispatcher()

	// Create a listener that returns a result
	type ResultListener struct {
		returnValue interface{}
		returnError error
		called      bool
	}

	listener1 := &TestListener{}
	listener2 := &TestListener{}

	d.Listen("test.event", listener1)
	d.Listen("test.event", listener2)

	// Until should process all listeners since they don't return values
	result, err := d.Until("test.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if result != nil {
		t.Error("Expected nil result when no listener returns a value")
	}

	if !listener1.WasHandled() || !listener2.WasHandled() {
		t.Error("Both listeners should have been called")
	}
}

func TestDispatcherDispatchNow(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	d.Listen("sync.event", listener)

	err := d.DispatchNow("sync.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !listener.WasHandled() {
		t.Error("Event should be handled synchronously")
	}
}

func TestDispatcherSetQueueDispatcher(t *testing.T) {
	d := NewDispatcher()

	// Create a mock queue dispatcher
	queue := &mockQueueDispatcher{}

	// Set queue dispatcher
	d.SetQueueDispatcher(queue)

	// Queue should be set
	if d.queue == nil {
		t.Error("Queue dispatcher should be set")
	}

	// Test dispatch with queue
	listener := &TestQueuedListener{}
	d.Listen("queued.event", listener)

	err := d.Dispatch("queued.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// Check event was queued
	time.Sleep(100 * time.Millisecond)
	if !listener.WasHandled() {
		t.Error("Queued event should have been processed")
	}
}

func TestDispatcherCreation(t *testing.T) {
	// NewDispatcher should return a new instance each time
	d1 := NewDispatcher()
	if d1 == nil {
		t.Fatal("NewDispatcher should return non-nil dispatcher")
	}

	d2 := NewDispatcher()
	if d2 == nil {
		t.Fatal("NewDispatcher should return non-nil dispatcher")
	}

	if d1 == d2 {
		t.Error("NewDispatcher should return different instances")
	}
}
