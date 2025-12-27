package events

import (
	"sync"
	"testing"
	"time"
)

func TestInitPackage(t *testing.T) {
	// Test Reset function
	Reset()

	// Get dispatcher to initialize it
	d1 := GetDispatcher()
	if d1 == nil {
		t.Fatal("GetDispatcher should initialize dispatcher")
	}

	// Reset and get new dispatcher
	Reset()
	d2 := GetDispatcher()

	if d2 == nil {
		t.Fatal("GetDispatcher should reinitialize after reset")
	}
}

func TestInitializeDispatcher(t *testing.T) {
	// Reset first
	Reset()

	// Create custom dispatcher
	custom := NewDispatcher()

	// Initialize with custom dispatcher
	Initialize(custom)

	// Get dispatcher should return the custom one
	if GetDispatcher() != custom {
		t.Error("GetDispatcher should return initialized dispatcher")
	}

	// Reset for other tests
	Reset()
}

func TestGlobalFunctions(t *testing.T) {
	// Reset to ensure clean state
	Reset()

	listener := &TestListener{}

	// Test Listen
	Listen("global.event", listener)
	if !HasListeners("global.event") {
		t.Error("Global Listen should register listener")
	}

	// Test GetListeners
	listeners := GetListeners("global.event")
	if len(listeners) != 1 {
		t.Errorf("Expected 1 listener, got %d", len(listeners))
	}

	// Test Subscribe
	subscriber := NewTestSubscriber()
	Subscribe(subscriber)
	if !HasListeners("user.registered") {
		t.Error("Subscribe should register subscriber's listeners")
	}

	// Test Dispatch
	err := Dispatch("global.event")
	if err != nil {
		t.Errorf("Global Dispatch failed: %v", err)
	}
	if !listener.WasHandled() {
		t.Error("Event should be handled")
	}

	// Test Forget
	Forget("global.event")
	if HasListeners("global.event") {
		t.Error("Forget should remove listeners")
	}

	// Test Flush
	Listen("temp.event", listener)
	Flush("temp.event")
	if HasListeners("temp.event") {
		t.Error("Flush should remove all listeners")
	}
}

func TestGlobalAsyncFunctions(t *testing.T) {
	Reset()

	listener := &TestListener{}
	Listen("async.event", listener)

	// Test DispatchAsync
	err := DispatchAsync("async.event")
	if err != nil {
		t.Errorf("DispatchAsync failed: %v", err)
	}

	// Test DispatchAfter
	err = DispatchAfter("async.event", 50*time.Millisecond)
	if err != nil {
		t.Errorf("DispatchAfter failed: %v", err)
	}
}

func TestFakeHelper(t *testing.T) {
	// Reset first
	Reset()

	// Use Fake helper
	fake := Fake()

	// Dispatch and assert
	Dispatch("test.event")

	err := fake.AssertDispatched("test.event", nil)
	if err != nil {
		t.Error("Fake should record dispatched events")
	}

	// Reset for other tests
	Reset()
}

func TestGlobalDispatcherConcurrency(t *testing.T) {
	Reset()

	var wg sync.WaitGroup
	errors := make(chan error, 100)

	// Concurrent operations on global dispatcher
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Listen("concurrent.event", &TestListener{})
		}(i)
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Dispatch("concurrent.event"); err != nil {
				errors <- err
			}
		}()
	}

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Forget("concurrent.event")
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("Concurrent operation error: %v", err)
	}
}
