package events

import (
	"errors"
	"sync"
	"testing"
)

// Test model
type TestUser struct {
	ID    int
	Name  string
	Email string
}

// Test observer
type TestUserObserver struct {
	BaseObserver
	mu        sync.Mutex
	events    []string
	models    []interface{}
	shouldErr map[string]bool
}

func NewTestUserObserver() *TestUserObserver {
	return &TestUserObserver{
		events:    make([]string, 0),
		models:    make([]interface{}, 0),
		shouldErr: make(map[string]bool),
	}
}

func (o *TestUserObserver) recordEvent(event string, model interface{}) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	o.events = append(o.events, event)
	o.models = append(o.models, model)

	if o.shouldErr[event] {
		return errors.New("observer error: " + event)
	}
	return nil
}

func (o *TestUserObserver) Creating(model interface{}) error {
	return o.recordEvent("creating", model)
}

func (o *TestUserObserver) Created(model interface{}) error {
	return o.recordEvent("created", model)
}

func (o *TestUserObserver) Updating(model interface{}) error {
	return o.recordEvent("updating", model)
}

func (o *TestUserObserver) Updated(model interface{}) error {
	return o.recordEvent("updated", model)
}

func (o *TestUserObserver) Saving(model interface{}) error {
	return o.recordEvent("saving", model)
}

func (o *TestUserObserver) Saved(model interface{}) error {
	return o.recordEvent("saved", model)
}

func (o *TestUserObserver) Deleting(model interface{}) error {
	return o.recordEvent("deleting", model)
}

func (o *TestUserObserver) Deleted(model interface{}) error {
	return o.recordEvent("deleted", model)
}

func TestObserverRegistry(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()

	// Register observer for User model
	registry.Observe("TestUser", observer)

	// Fire creating event
	user := &TestUser{ID: 1, Name: "John", Email: "john@example.com"}
	err := registry.Fire("creating", user)
	if err != nil {
		t.Errorf("Fire failed: %v", err)
	}

	// Check event was recorded
	if len(observer.events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(observer.events))
	}
	if observer.events[0] != "creating" {
		t.Errorf("Expected 'creating' event, got %s", observer.events[0])
	}

	// Test multiple events
	registry.Fire("created", user)
	registry.Fire("updating", user)
	registry.Fire("updated", user)

	if len(observer.events) != 4 {
		t.Errorf("Expected 4 events, got %d", len(observer.events))
	}
}

func TestObserverRegistryWithMultipleObservers(t *testing.T) {
	registry := NewObserverRegistry()
	observer1 := NewTestUserObserver()
	observer2 := NewTestUserObserver()

	// Register multiple observers
	registry.Observe("TestUser", observer1)
	registry.Observe("TestUser", observer2)

	// Fire event
	user := &TestUser{ID: 1}
	registry.Fire("creating", user)

	// Both observers should receive the event
	if len(observer1.events) != 1 {
		t.Error("Observer 1 did not receive event")
	}
	if len(observer2.events) != 1 {
		t.Error("Observer 2 did not receive event")
	}
}

func TestObserverRegistryError(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()
	observer.shouldErr["creating"] = true

	registry.Observe("TestUser", observer)

	// Fire event that will error
	user := &TestUser{ID: 1}
	err := registry.Fire("creating", user)

	if err == nil {
		t.Error("Expected error from observer")
	}
}

func TestObserverRegistryUnknownEvent(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()
	registry.Observe("TestUser", observer)

	user := &TestUser{ID: 1}
	err := registry.Fire("unknown", user)

	if err == nil {
		t.Error("Expected error for unknown event")
	}
}

func TestObserverRegistryModelTypeExtraction(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()

	// Register using model instance
	user := &TestUser{}
	registry.ObserveModel(user, observer)

	// Fire event
	registry.Fire("creating", user)

	if len(observer.events) != 1 {
		t.Error("Observer was not called")
	}
}

func TestObserverRegistryClear(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()

	registry.Observe("TestUser", observer)

	// Clear observers for TestUser
	registry.ClearObservers("TestUser")

	// Fire event - should not be received
	user := &TestUser{}
	registry.Fire("creating", user)

	if len(observer.events) != 0 {
		t.Error("Observer should not have received event after clear")
	}
}

func TestObserverRegistryClearAll(t *testing.T) {
	registry := NewObserverRegistry()
	observer1 := NewTestUserObserver()
	observer2 := NewTestUserObserver()

	registry.Observe("TestUser", observer1)
	registry.Observe("TestOrder", observer2)

	// Clear all
	registry.ClearAll()

	// Fire events - should not be received
	user := &TestUser{}
	registry.Fire("creating", user)

	if len(observer1.events) != 0 || len(observer2.events) != 0 {
		t.Error("Observers should not have received events after clear all")
	}
}

func TestBaseObserver(t *testing.T) {
	observer := &BaseObserver{}
	user := &TestUser{}

	// All methods should return nil
	if observer.Creating(user) != nil {
		t.Error("BaseObserver.Creating should return nil")
	}
	if observer.Created(user) != nil {
		t.Error("BaseObserver.Created should return nil")
	}
	if observer.Updating(user) != nil {
		t.Error("BaseObserver.Updating should return nil")
	}
	if observer.Updated(user) != nil {
		t.Error("BaseObserver.Updated should return nil")
	}
	if observer.Saving(user) != nil {
		t.Error("BaseObserver.Saving should return nil")
	}
	if observer.Saved(user) != nil {
		t.Error("BaseObserver.Saved should return nil")
	}
	if observer.Deleting(user) != nil {
		t.Error("BaseObserver.Deleting should return nil")
	}
	if observer.Deleted(user) != nil {
		t.Error("BaseObserver.Deleted should return nil")
	}
	if observer.Restoring(user) != nil {
		t.Error("BaseObserver.Restoring should return nil")
	}
	if observer.Restored(user) != nil {
		t.Error("BaseObserver.Restored should return nil")
	}
}

func TestObservableDispatcher(t *testing.T) {
	dispatcher := NewObservableDispatcher()
	observer := NewTestUserObserver()

	// Register observer
	dispatcher.Observe("TestUser", observer)

	// Also register a regular event listener
	var regularEventFired bool
	dispatcher.Listen("testuser.created", &SimpleListener{
		HandleFunc: func(event interface{}) error {
			regularEventFired = true
			return nil
		},
	})

	// Fire model event
	user := &TestUser{ID: 1, Name: "John"}
	err := dispatcher.FireModelEvent("created", user)
	if err != nil {
		t.Errorf("FireModelEvent failed: %v", err)
	}

	// Check observer was called
	if len(observer.events) != 1 {
		t.Error("Observer was not called")
	}

	// Check regular event was also dispatched
	if !regularEventFired {
		t.Error("Regular event was not dispatched")
	}
}

func TestModelEvent(t *testing.T) {
	event := &ModelEvent{
		BaseEvent: BaseEvent{EventName: "user.created"},
		Model:     &TestUser{ID: 1},
		Action:    "created",
		ModelType: "TestUser",
	}

	if event.Name() != "user.created" {
		t.Errorf("Expected event name 'user.created', got %s", event.Name())
	}
}

// Test auto observer with custom type
type CustomUserObserver struct {
	creatingCalled bool
	createdCalled  bool
	updatingCalled bool
}

func (o *CustomUserObserver) Creating(model interface{}) error {
	o.creatingCalled = true
	return nil
}

func (o *CustomUserObserver) Updated(model interface{}) error {
	// Intentionally not implementing Created but implementing Updated
	return errors.New("updated error")
}

func TestAutoObserver(t *testing.T) {
	observer := &CustomUserObserver{}
	autoObserver := NewAutoObserver(observer)

	user := &TestUser{}

	// Call Creating - should work
	err := autoObserver.Creating(user)
	if err != nil {
		t.Errorf("Creating failed: %v", err)
	}
	if !observer.creatingCalled {
		t.Error("Creating method was not called")
	}

	// Call Created - should use base implementation (no error)
	err = autoObserver.Created(user)
	if err != nil {
		t.Errorf("Created failed: %v", err)
	}

	// Call Updated - should return error
	err = autoObserver.Updated(user)
	if err == nil {
		t.Error("Expected error from Updated")
	}

	// Call method that doesn't exist - should use base
	err = autoObserver.Deleting(user)
	if err != nil {
		t.Error("Deleting should use base implementation")
	}
}

func TestConditionalObserver(t *testing.T) {
	innerObserver := NewTestUserObserver()

	// Only fire for creating and created events
	condition := func(event string, model interface{}) bool {
		return event == "creating" || event == "created"
	}

	observer := NewConditionalObserver(innerObserver, condition)

	user := &TestUser{}

	// These should fire
	observer.Creating(user)
	observer.Created(user)

	// These should not fire
	observer.Updating(user)
	observer.Updated(user)
	observer.Deleting(user)

	// Check only 2 events were recorded
	if len(innerObserver.events) != 2 {
		t.Errorf("Expected 2 events, got %d", len(innerObserver.events))
	}

	if innerObserver.events[0] != "creating" || innerObserver.events[1] != "created" {
		t.Error("Wrong events recorded")
	}
}

func TestGlobalObserver(t *testing.T) {
	// Clear any existing global observers
	globalRegistry.ClearAll()

	observer := NewTestUserObserver()

	// Register globally
	ObserveGlobal("TestUser", observer)

	// Fire global event
	user := &TestUser{ID: 1}
	err := FireModelEvent("creating", user)
	if err != nil {
		t.Errorf("FireModelEvent failed: %v", err)
	}

	if len(observer.events) != 1 {
		t.Error("Global observer was not called")
	}

	// Clean up
	globalRegistry.ClearAll()
}

func TestObserverConcurrency(t *testing.T) {
	registry := NewObserverRegistry()
	observer := NewTestUserObserver()

	registry.Observe("TestUser", observer)

	// Fire events concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			user := &TestUser{ID: id}
			registry.Fire("creating", user)
		}(i)
	}

	wg.Wait()

	// Should have 100 events
	if len(observer.events) != 100 {
		t.Errorf("Expected 100 events, got %d", len(observer.events))
	}
}

// Benchmark tests
func BenchmarkObserverFire(b *testing.B) {
	registry := NewObserverRegistry()
	observer := &BaseObserver{}
	registry.Observe("TestUser", observer)

	user := &TestUser{ID: 1}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Fire("creating", user)
	}
}

func BenchmarkAutoObserver(b *testing.B) {
	observer := &CustomUserObserver{}
	autoObserver := NewAutoObserver(observer)

	user := &TestUser{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		autoObserver.Creating(user)
	}
}

func BenchmarkConditionalObserver(b *testing.B) {
	innerObserver := &BaseObserver{}
	condition := func(event string, model interface{}) bool {
		return event == "creating"
	}
	observer := NewConditionalObserver(innerObserver, condition)

	user := &TestUser{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		observer.Creating(user)
	}
}
