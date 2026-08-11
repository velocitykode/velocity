package events

import (
	"sync"
	"testing"
)

func TestEventRegistry(t *testing.T) {
	t.Run("creates new registry", func(t *testing.T) {
		registry := NewEventRegistry()
		if registry == nil {
			t.Error("Expected registry to be created")
		}
		if len(registry.GetAllEvents()) != 0 {
			t.Error("Expected new registry to be empty")
		}
	})

	t.Run("registers listeners", func(t *testing.T) {
		registry := NewEventRegistry()
		registry.Register("user.created", "UserListener.HandleUserCreated")

		listeners := registry.GetListeners("user.created")
		if len(listeners) != 1 {
			t.Errorf("Expected 1 listener, got %d", len(listeners))
		}
		if listeners[0] != "UserListener.HandleUserCreated" {
			t.Errorf("Expected UserListener.HandleUserCreated, got %s", listeners[0])
		}
	})

	t.Run("registers multiple listeners for same event", func(t *testing.T) {
		registry := NewEventRegistry()
		registry.Register("user.created", "EmailListener")
		registry.Register("user.created", "LogListener")

		listeners := registry.GetListeners("user.created")
		if len(listeners) != 2 {
			t.Errorf("Expected 2 listeners, got %d", len(listeners))
		}
	})

	t.Run("gets all events", func(t *testing.T) {
		registry := NewEventRegistry()
		registry.Register("user.created", "Listener1")
		registry.Register("order.placed", "Listener2")

		events := registry.GetAllEvents()
		if len(events) != 2 {
			t.Errorf("Expected 2 events, got %d", len(events))
		}
	})

	t.Run("counts registrations", func(t *testing.T) {
		registry := NewEventRegistry()
		registry.Register("user.created", "Listener1")
		registry.Register("user.created", "Listener2")
		registry.Register("order.placed", "Listener3")

		count := registry.Count()
		if count != 3 {
			t.Errorf("Expected count 3, got %d", count)
		}
	})

	t.Run("clears registrations", func(t *testing.T) {
		registry := NewEventRegistry()
		registry.Register("user.created", "Listener1")
		registry.Clear()

		if registry.Count() != 0 {
			t.Error("Expected registry to be cleared")
		}
	})
}

func TestEventDiscovery(t *testing.T) {
	t.Run("discovers from subscriber with pointer receiver methods", func(t *testing.T) {
		registry := NewEventRegistry()
		subscriber := &TestDiscoverySubscriber{}

		discovered := registry.DiscoverFromType(subscriber)

		if len(discovered) != 3 {
			t.Errorf("Expected 3 discovered events, got %d", len(discovered))
		}

		if _, ok := discovered["user.registered"]; !ok {
			t.Error("Expected user.registered to be discovered")
		}
		if _, ok := discovered["order.placed"]; !ok {
			t.Error("Expected order.placed to be discovered")
		}
		if _, ok := discovered["payment.processed"]; !ok {
			t.Error("Expected payment.processed to be discovered")
		}
	})

	t.Run("auto-registers discovered events", func(t *testing.T) {
		registry := NewEventRegistry()
		subscriber := &TestDiscoverySubscriber{}

		registry.DiscoverFromType(subscriber)

		listeners := registry.GetListeners("user.registered")
		if len(listeners) != 1 {
			t.Errorf("Expected 1 listener for user.registered, got %d", len(listeners))
		}
		if listeners[0] != "TestDiscoverySubscriber.HandleUserRegistered" {
			t.Errorf("Unexpected listener name: %s", listeners[0])
		}
	})

	t.Run("extracts event names correctly", func(t *testing.T) {
		tests := []struct {
			methodName string
			eventName  string
		}{
			{"HandleUserRegistered", "user.registered"},
			{"HandleOrderPlaced", "order.placed"},
			{"HandlePaymentProcessed", "payment.processed"},
			{"HandleSomethingVeryLong", "something.very.long"},
			{"Handle", ""},
			{"NotHandle", ""},
		}

		for _, tt := range tests {
			result := extractEventName(tt.methodName)
			if result != tt.eventName {
				t.Errorf("extractEventName(%s) = %s, expected %s",
					tt.methodName, result, tt.eventName)
			}
		}
	})
}

func TestEventModule(t *testing.T) {
	t.Run("adds and boots modules", func(t *testing.T) {
		registry := NewEventRegistry()
		dispatcher := NewDispatcher()
		module := &TestModule{registered: false}

		registry.AddModule(module)
		registry.BootModules(dispatcher)

		if !module.registered {
			t.Error("Expected module to be registered")
		}
	})

	t.Run("boots multiple modules", func(t *testing.T) {
		registry := NewEventRegistry()
		dispatcher := NewDispatcher()

		module1 := &TestModule{}
		module2 := &TestModule{}

		registry.AddModule(module1)
		registry.AddModule(module2)
		registry.BootModules(dispatcher)

		if !module1.registered || !module2.registered {
			t.Error("Expected all modules to be registered")
		}
	})
}

func TestDiscoveryConcurrency(t *testing.T) {
	t.Run("concurrent registrations", func(t *testing.T) {
		registry := NewEventRegistry()
		var wg sync.WaitGroup

		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				registry.Register("test.event", "Listener")
			}(i)
		}

		wg.Wait()

		listeners := registry.GetListeners("test.event")
		if len(listeners) != 100 {
			t.Errorf("Expected 100 listeners, got %d", len(listeners))
		}
	})

	t.Run("concurrent discoveries", func(t *testing.T) {
		registry := NewEventRegistry()
		var wg sync.WaitGroup

		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				subscriber := &TestDiscoverySubscriber{}
				registry.DiscoverFromType(subscriber)
			}()
		}

		wg.Wait()

		listeners := registry.GetListeners("user.registered")
		if len(listeners) != 50 {
			t.Errorf("Expected 50 listeners, got %d", len(listeners))
		}
	})

	t.Run("concurrent reads and writes", func(t *testing.T) {
		registry := NewEventRegistry()
		var wg sync.WaitGroup

		// Writers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				registry.Register("test.event", "Listener")
			}()
		}

		// Readers
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				registry.GetListeners("test.event")
				registry.GetAllEvents()
				registry.Count()
			}()
		}

		wg.Wait()
	})
}

// Test helpers
type TestDiscoverySubscriber struct{}

func (s *TestDiscoverySubscriber) HandleUserRegistered(event interface{}) error {
	return nil
}

func (s *TestDiscoverySubscriber) HandleOrderPlaced(event interface{}) error {
	return nil
}

func (s *TestDiscoverySubscriber) HandlePaymentProcessed(event interface{}) error {
	return nil
}

type TestModule struct {
	registered bool
}

func (m *TestModule) Register(dispatcher Dispatcher) {
	m.registered = true
}

// Benchmarks
func BenchmarkDiscovery(b *testing.B) {
	registry := NewEventRegistry()
	subscriber := &TestDiscoverySubscriber{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.DiscoverFromType(subscriber)
	}
}

func BenchmarkRegistration(b *testing.B) {
	registry := NewEventRegistry()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		registry.Register("test.event", "TestListener")
	}
}
