package events

import (
	"sync"
	"testing"
	"time"
)

func TestDispatcherFlush(t *testing.T) {
	d := NewDispatcher()
	listener1 := &TestListener{}
	listener2 := &TestListener{}

	d.Listen("event1", listener1)
	d.Listen("event2", listener2)

	if !d.HasListeners("event1") || !d.HasListeners("event2") {
		t.Error("Expected listeners to be registered")
	}

	d.Flush("event1")
	d.Flush("event2")

	if d.HasListeners("event1") || d.HasListeners("event2") {
		t.Error("Expected no listeners after flush")
	}
}

func TestDispatcherStringEvent(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	d.Listen("string.event", listener)

	err := d.Dispatch("string.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !listener.WasHandled() {
		t.Error("Expected listener to handle string event")
	}
}

func TestDispatcherMultipleEventsToSameListener(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	events := []string{"event1", "event2", "event3"}
	d.Listen(events, listener)

	for _, event := range events {
		if !d.HasListeners(event) {
			t.Errorf("Expected listener for event %s", event)
		}
	}
}

func TestDispatcherForgetWildcard(t *testing.T) {
	d := NewDispatcher()
	listener := &TestListener{}

	d.Listen("user.*", listener)

	if !d.HasListeners("user.created") {
		t.Error("Expected wildcard listener to match")
	}

	d.Forget("user.*")

	if d.HasListeners("user.created") {
		t.Error("Expected no wildcard listeners after forget")
	}
}

func TestDispatcherGetListeners(t *testing.T) {
	d := NewDispatcher()
	listener1 := &TestListener{}
	listener2 := &TestListener{}

	d.Listen("test.event", listener1)
	d.Listen("test.event", listener2)

	listeners := d.GetListeners("test.event")
	if len(listeners) != 2 {
		t.Errorf("Expected 2 listeners, got %d", len(listeners))
	}
}

func TestDispatcherWildcardVariations(t *testing.T) {
	d := NewDispatcher()

	tests := []struct {
		pattern string
		event   string
		should  bool
	}{
		{"*", "any.event", true},
		{"user.*", "user.created", true},
		{"user.*", "user.updated", true},
		{"user.*", "order.created", false},
		{"*.created", "user.created", true},
		{"*.created", "order.created", true},
		{"*.created", "user.updated", false},
		// Middle wildcards not supported - this would actually match in our simple implementation
		{"user.*.created", "user.profile.created", true}, // Will match as user.* prefix
		{"exact.match", "exact.match", true},
		{"exact.match", "exact.no.match", false},
	}

	for _, test := range tests {
		listener := &TestListener{}
		d.Listen(test.pattern, listener)

		d.Dispatch(test.event)

		if test.should && !listener.WasHandled() {
			t.Errorf("Pattern %s should match %s", test.pattern, test.event)
		}
		if !test.should && listener.WasHandled() {
			t.Errorf("Pattern %s should not match %s", test.pattern, test.event)
		}

		d.Forget(test.pattern) // Clear for next test
	}
}

func TestDispatcherNilEvent(t *testing.T) {
	d := NewDispatcher()
	err := d.Dispatch(nil)
	if err == nil {
		t.Fatal("expected error when dispatching nil event")
	}
}

func TestDispatcherListenerPanic(t *testing.T) {
	d := NewDispatcher()

	d.Listen("test.panic", &panicListener{})

	err := d.Dispatch("test.panic")
	if err == nil {
		t.Fatal("expected error from panicking listener")
	}
}

type panicListener struct{}

func (p *panicListener) Handle(event interface{}) error { panic("listener blew up") }
func (p *panicListener) ShouldQueue() bool              { return false }

func TestDispatcherConcurrentModification(t *testing.T) {
	d := NewDispatcher()

	var wg sync.WaitGroup

	// Concurrent adds
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d.Listen("event", &TestListener{})
		}(i)
	}

	// Concurrent dispatches
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Dispatch("event")
		}()
	}

	// Concurrent forget
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(5 * time.Millisecond)
		d.Forget("event")
	}()

	wg.Wait()
}

// PanicListener for testing panic recovery
type PanicListener struct{}

func (l *PanicListener) Handle(event interface{}) error {
	panic("listener panic")
}

func (l *PanicListener) ShouldQueue() bool {
	return false
}

// TestQueuedListener for testing queued events
type TestQueuedListener struct {
	handled bool
	mu      sync.Mutex
}

func (l *TestQueuedListener) Handle(event interface{}) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.handled = true
	return nil
}

func (l *TestQueuedListener) ShouldQueue() bool {
	return true
}

func (l *TestQueuedListener) WasHandled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.handled
}

func TestDispatcherQueuedListener(t *testing.T) {
	d := NewDispatcher()
	listener := &TestQueuedListener{}

	d.Listen("test.event", listener)

	// Without queue, should still handle via goroutine
	err := d.Dispatch("test.event")
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if !listener.WasHandled() {
		t.Error("Expected queued listener to be handled via goroutine")
	}
}

func TestDispatcherErrorHandling(t *testing.T) {
	d := NewDispatcher()

	errorListener := &TestListener{shouldErr: true}
	normalListener := &TestListener{}

	d.Listen("test.event", errorListener)
	d.Listen("test.event", normalListener)

	err := d.Dispatch("test.event")
	if err == nil {
		t.Error("Expected error from listener")
	}

	// After aggregation: the dispatcher no longer short-circuits — every
	// listener is invoked and errors are joined so one bad listener cannot
	// silently prevent the rest from running.
	if !normalListener.WasHandled() {
		t.Error("Listener should still be called after another listener errors")
	}
}

func TestGetEventName(t *testing.T) {
	d := NewDispatcher()
	tests := []struct {
		event    interface{}
		expected string
	}{
		{"string.event", "string.event"},
		{UserRegistered{}, "user.registered"},
		{&UserRegistered{}, "user.registered"},
		{OrderPlaced{}, "order.placed"},
		{&OrderPlaced{}, "order.placed"},
	}

	for _, test := range tests {
		name := d.getEventName(test.event)
		if name != test.expected {
			t.Errorf("Expected %s, got %s for %T", test.expected, name, test.event)
		}
	}
}

func TestMatchesPattern(t *testing.T) {
	d := NewDispatcher()
	tests := []struct {
		pattern  string
		name     string
		expected bool
	}{
		{"*", "anything", true},
		{"user.*", "user.created", true},
		{"user.*", "user.", true}, // Will match since user. is prefix of user.*
		{"user.*", "users.created", false},
		{"*.created", "user.created", true},
		{"*.created", ".created", true}, // Will match as suffix
		{"*created", "user.created", true},
		{"exact", "exact", true},
		{"exact", "not.exact", false},
		{"", "anything", false},
		{"pattern", "", false},
	}

	for _, test := range tests {
		result := d.matchesPattern(test.name, test.pattern)
		if result != test.expected {
			t.Errorf("matchesPattern(%s, %s) = %v, expected %v",
				test.pattern, test.name, result, test.expected)
		}
	}
}
