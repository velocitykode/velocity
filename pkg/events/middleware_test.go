package events

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestMiddlewareDispatcher(t *testing.T) {
	t.Run("creates middleware dispatcher", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		if dispatcher == nil {
			t.Error("Expected dispatcher to be created")
		}
	})

	t.Run("dispatches without middleware", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		handled := false

		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			handled = true
		}})

		err := dispatcher.Dispatch(&middlewareTestEvent{})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if !handled {
			t.Error("Event should have been handled")
		}
	})

	t.Run("dispatches through middleware", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		middlewareRan := false

		dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
			middlewareRan = true
			return next(event)
		})

		dispatcher.Listen("test.event", &middlewareTestListener{})
		dispatcher.Dispatch(&middlewareTestEvent{})

		if !middlewareRan {
			t.Error("Middleware should have run")
		}
	})

	t.Run("middleware chain executes in order", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		order := []int{}
		mu := sync.Mutex{}

		dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return next(event)
		})

		dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
			mu.Lock()
			order = append(order, 2)
			mu.Unlock()
			return next(event)
		})

		dispatcher.Listen("test.event", &middlewareTestListener{})
		dispatcher.Dispatch(&middlewareTestEvent{})

		mu.Lock()
		if len(order) != 2 || order[0] != 1 || order[1] != 2 {
			t.Errorf("Expected order [1, 2], got %v", order)
		}
		mu.Unlock()
	})

	t.Run("get and clear middleware", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
			return next(event)
		})

		middlewares := dispatcher.GetMiddleware()
		if len(middlewares) != 1 {
			t.Errorf("Expected 1 middleware, got %d", len(middlewares))
		}

		dispatcher.ClearMiddleware()
		middlewares = dispatcher.GetMiddleware()
		if len(middlewares) != 0 {
			t.Error("Expected middleware to be cleared")
		}
	})
}

func TestLoggingMiddleware(t *testing.T) {
	t.Run("logs events", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		logger := NewLoggingMiddleware()

		dispatcher.Use(logger)
		dispatcher.Listen("test.event", &middlewareTestListener{})
		dispatcher.Dispatch(&middlewareTestEvent{})

		log := logger.GetLog()
		if len(log) != 1 {
			t.Errorf("Expected 1 log entry, got %d", len(log))
		}
	})

	t.Run("clears log", func(t *testing.T) {
		logger := NewLoggingMiddleware()
		logger.log = append(logger.log, "test")

		logger.ClearLog()

		if len(logger.GetLog()) != 0 {
			t.Error("Expected log to be cleared")
		}
	})
}

func TestValidationMiddleware(t *testing.T) {
	t.Run("validates events", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		validator := NewValidationMiddleware(func(event interface{}) error {
			if _, ok := event.(*middlewareTestEvent); !ok {
				return fmt.Errorf("invalid event type")
			}
			return nil
		})

		dispatcher.Use(validator)
		dispatcher.Listen("test.event", &middlewareTestListener{})

		err := dispatcher.Dispatch(&middlewareTestEvent{})
		if err != nil {
			t.Errorf("Valid event should pass: %v", err)
		}
	})

	t.Run("rejects invalid events", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		validator := NewValidationMiddleware(func(event interface{}) error {
			return fmt.Errorf("validation failed")
		})

		dispatcher.Use(validator)
		err := dispatcher.Dispatch(&middlewareTestEvent{})

		if err == nil {
			t.Error("Expected validation error")
		}
	})
}

func TestTransformMiddleware(t *testing.T) {
	t.Run("transforms events", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		var receivedEvent interface{}

		transformer := NewTransformMiddleware(func(event interface{}) interface{} {
			if e, ok := event.(*middlewareTestEvent); ok {
				// Transform by creating a modified copy
				transformed := &middlewareTestEvent{name: "test.event"}
				// Store some transformation marker in the event
				_ = e // original event
				return transformed
			}
			return event
		})

		dispatcher.Use(transformer)
		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			// Track that handler was called
			receivedEvent = "handled"
		}})

		dispatcher.Dispatch(&middlewareTestEvent{})

		if receivedEvent == nil {
			t.Error("Event should have been transformed and handled")
		}
	})
}

func TestFilterMiddleware(t *testing.T) {
	t.Run("filters events", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		handled := false

		filter := NewFilterMiddleware(func(event interface{}) bool {
			return false // Block all events
		})

		dispatcher.Use(filter)
		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			handled = true
		}})

		dispatcher.Dispatch(&middlewareTestEvent{})

		if handled {
			t.Error("Filtered event should not be handled")
		}
	})

	t.Run("allows events that pass filter", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		handled := false

		filter := NewFilterMiddleware(func(event interface{}) bool {
			return true // Allow all events
		})

		dispatcher.Use(filter)
		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			handled = true
		}})

		dispatcher.Dispatch(&middlewareTestEvent{})

		if !handled {
			t.Error("Allowed event should be handled")
		}
	})
}

func TestTimingMiddleware(t *testing.T) {
	t.Run("measures dispatch time", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		timer := NewTimingMiddleware()

		dispatcher.Use(timer)
		dispatcher.Listen("test.event", &middlewareTestListener{})
		dispatcher.Dispatch(&middlewareTestEvent{})

		timing := timer.GetTiming("test.event")
		if timing == 0 {
			t.Error("Expected timing to be measured")
		}
	})

	t.Run("gets all timings", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		timer := NewTimingMiddleware()

		dispatcher.Use(timer)
		dispatcher.Listen("test.event1", &middlewareTestListener{})
		dispatcher.Listen("test.event2", &middlewareTestListener{})

		dispatcher.Dispatch(&middlewareTestEvent{name: "test.event1"})
		dispatcher.Dispatch(&middlewareTestEvent{name: "test.event2"})

		timings := timer.GetAllTimings()
		if len(timings) != 2 {
			t.Errorf("Expected 2 timings, got %d", len(timings))
		}
	})
}

func TestRetryMiddleware(t *testing.T) {
	t.Run("retries failed dispatches", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		attempts := 0

		retry := NewRetryMiddleware(3, 10*time.Millisecond)
		dispatcher.Use(retry)

		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			attempts++
			if attempts < 3 {
				panic("fail") // Simulate failure
			}
		}})

		dispatcher.Dispatch(&middlewareTestEvent{})

		if attempts != 3 {
			t.Errorf("Expected 3 attempts, got %d", attempts)
		}
	})

	t.Run("returns error after max retries", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()

		retry := NewRetryMiddleware(2, 1*time.Millisecond)
		dispatcher.Use(retry)

		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			panic("always fail")
		}})

		err := dispatcher.Dispatch(&middlewareTestEvent{})

		if err == nil {
			t.Error("Expected error after max retries")
		}
	})

	t.Run("tracks attempts", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		retry := NewRetryMiddleware(3, 1*time.Millisecond)

		dispatcher.Use(retry)
		dispatcher.Listen("test.event", &middlewareTestListener{onHandle: func() {
			panic("fail")
		}})

		dispatcher.Dispatch(&middlewareTestEvent{})

		attempts := retry.GetAttempts("test.event")
		if attempts != 4 { // Initial attempt + 3 retries
			t.Errorf("Expected 4 attempts, got %d", attempts)
		}
	})
}

func TestMiddlewareConcurrency(t *testing.T) {
	t.Run("concurrent middleware dispatches", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		logger := NewLoggingMiddleware()

		dispatcher.Use(logger)
		dispatcher.Listen("test.event", &middlewareTestListener{})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dispatcher.Dispatch(&middlewareTestEvent{})
			}()
		}

		wg.Wait()

		log := logger.GetLog()
		if len(log) != 100 {
			t.Errorf("Expected 100 log entries, got %d", len(log))
		}
	})

	t.Run("concurrent middleware additions", func(t *testing.T) {
		dispatcher := NewMiddlewareDispatcher()
		var wg sync.WaitGroup

		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
					return next(event)
				})
			}()
		}

		wg.Wait()

		middlewares := dispatcher.GetMiddleware()
		if len(middlewares) != 50 {
			t.Errorf("Expected 50 middlewares, got %d", len(middlewares))
		}
	})
}

// Test Helpers
type middlewareTestListener struct {
	onHandle func()
}

func (l *middlewareTestListener) Handle(event interface{}) error {
	if l.onHandle != nil {
		l.onHandle()
	}
	return nil
}

func (l *middlewareTestListener) ShouldQueue() bool {
	return false
}

type middlewareTestEvent struct {
	name string
}

func (e *middlewareTestEvent) Name() string {
	if e.name != "" {
		return e.name
	}
	return "test.event"
}

// Benchmarks
func BenchmarkMiddlewareDispatch(b *testing.B) {
	dispatcher := NewMiddlewareDispatcher()
	dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
		return next(event)
	})
	dispatcher.Listen("test.event", &middlewareTestListener{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(&middlewareTestEvent{})
	}
}

func BenchmarkMiddlewareChain(b *testing.B) {
	dispatcher := NewMiddlewareDispatcher()

	// Add 5 middlewares
	for i := 0; i < 5; i++ {
		dispatcher.UseFunc(func(event interface{}, next func(interface{}) error) error {
			return next(event)
		})
	}

	dispatcher.Listen("test.event", &middlewareTestListener{})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(&middlewareTestEvent{})
	}
}
