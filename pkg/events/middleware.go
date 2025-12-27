package events

import (
	"fmt"
	"sync"
	"time"
)

// EventMiddleware processes events before they reach listeners
type EventMiddleware interface {
	// Handle processes the event and calls the next middleware
	Handle(event interface{}, next func(interface{}) error) error
}

// MiddlewareFunc adapts a function to the EventMiddleware interface
type MiddlewareFunc func(event interface{}, next func(interface{}) error) error

// Handle implements EventMiddleware
func (f MiddlewareFunc) Handle(event interface{}, next func(interface{}) error) error {
	return f(event, next)
}

// MiddlewareDispatcher wraps a dispatcher with middleware support
type MiddlewareDispatcher struct {
	*DefaultDispatcher
	mu          sync.RWMutex
	middlewares []EventMiddleware
}

// NewMiddlewareDispatcher creates a new middleware-enabled dispatcher
func NewMiddlewareDispatcher() *MiddlewareDispatcher {
	return &MiddlewareDispatcher{
		DefaultDispatcher: NewDispatcher(),
		middlewares:       make([]EventMiddleware, 0),
	}
}

// Use adds middleware to the dispatcher
func (d *MiddlewareDispatcher) Use(middleware EventMiddleware) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.middlewares = append(d.middlewares, middleware)
}

// UseFunc adds a middleware function
func (d *MiddlewareDispatcher) UseFunc(fn func(event interface{}, next func(interface{}) error) error) {
	d.Use(MiddlewareFunc(fn))
}

// Dispatch dispatches an event through middleware chain
func (d *MiddlewareDispatcher) Dispatch(event interface{}) error {
	d.mu.RLock()
	middlewares := make([]EventMiddleware, len(d.middlewares))
	copy(middlewares, d.middlewares)
	d.mu.RUnlock()

	// Build middleware chain
	handler := func(e interface{}) error {
		return d.DefaultDispatcher.Dispatch(e)
	}

	// Wrap in middleware from last to first
	for i := len(middlewares) - 1; i >= 0; i-- {
		currentMiddleware := middlewares[i]
		nextHandler := handler
		handler = func(e interface{}) error {
			return currentMiddleware.Handle(e, nextHandler)
		}
	}

	return handler(event)
}

// GetMiddleware returns all registered middleware
func (d *MiddlewareDispatcher) GetMiddleware() []EventMiddleware {
	d.mu.RLock()
	defer d.mu.RUnlock()
	middlewares := make([]EventMiddleware, len(d.middlewares))
	copy(middlewares, d.middlewares)
	return middlewares
}

// ClearMiddleware removes all middleware
func (d *MiddlewareDispatcher) ClearMiddleware() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.middlewares = make([]EventMiddleware, 0)
}

// Common Middleware Implementations

// LoggingMiddleware logs all events
type LoggingMiddleware struct {
	mu  sync.Mutex
	log []string
}

// NewLoggingMiddleware creates a new logging middleware
func NewLoggingMiddleware() *LoggingMiddleware {
	return &LoggingMiddleware{
		log: make([]string, 0),
	}
}

// Handle logs the event
func (m *LoggingMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	m.mu.Lock()
	eventName := getEventNameFromEvent(event)
	timestamp := time.Now().Format("15:04:05.000")
	m.log = append(m.log, fmt.Sprintf("[%s] Event dispatched: %s", timestamp, eventName))
	m.mu.Unlock()

	return next(event)
}

// GetLog returns the event log
func (m *LoggingMiddleware) GetLog() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	log := make([]string, len(m.log))
	copy(log, m.log)
	return log
}

// ClearLog clears the event log
func (m *LoggingMiddleware) ClearLog() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = make([]string, 0)
}

// ValidationMiddleware validates events before dispatching
type ValidationMiddleware struct {
	validator func(interface{}) error
}

// NewValidationMiddleware creates a new validation middleware
func NewValidationMiddleware(validator func(interface{}) error) *ValidationMiddleware {
	return &ValidationMiddleware{
		validator: validator,
	}
}

// Handle validates the event
func (m *ValidationMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	if err := m.validator(event); err != nil {
		return fmt.Errorf("event validation failed: %w", err)
	}
	return next(event)
}

// TransformMiddleware transforms events before dispatching
type TransformMiddleware struct {
	transformer func(interface{}) interface{}
}

// NewTransformMiddleware creates a new transform middleware
func NewTransformMiddleware(transformer func(interface{}) interface{}) *TransformMiddleware {
	return &TransformMiddleware{
		transformer: transformer,
	}
}

// Handle transforms the event
func (m *TransformMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	transformed := m.transformer(event)
	return next(transformed)
}

// FilterMiddleware filters events based on condition
type FilterMiddleware struct {
	condition func(interface{}) bool
}

// NewFilterMiddleware creates a new filter middleware
func NewFilterMiddleware(condition func(interface{}) bool) *FilterMiddleware {
	return &FilterMiddleware{
		condition: condition,
	}
}

// Handle filters the event
func (m *FilterMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	if !m.condition(event) {
		return nil // Skip event
	}
	return next(event)
}

// TimingMiddleware measures event dispatch time
type TimingMiddleware struct {
	mu      sync.Mutex
	timings map[string]time.Duration
}

// NewTimingMiddleware creates a new timing middleware
func NewTimingMiddleware() *TimingMiddleware {
	return &TimingMiddleware{
		timings: make(map[string]time.Duration),
	}
}

// Handle measures dispatch time
func (m *TimingMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	start := time.Now()
	err := next(event)
	duration := time.Since(start)

	eventName := getEventNameFromEvent(event)
	m.mu.Lock()
	m.timings[eventName] = duration
	m.mu.Unlock()

	return err
}

// GetTiming returns the timing for an event
func (m *TimingMiddleware) GetTiming(eventName string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.timings[eventName]
}

// GetAllTimings returns all timings
func (m *TimingMiddleware) GetAllTimings() map[string]time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	timings := make(map[string]time.Duration, len(m.timings))
	for k, v := range m.timings {
		timings[k] = v
	}
	return timings
}

// RetryMiddleware retries failed event dispatches
type RetryMiddleware struct {
	maxRetries int
	delay      time.Duration
	mu         sync.Mutex
	attempts   map[string]int
}

// NewRetryMiddleware creates a new retry middleware
func NewRetryMiddleware(maxRetries int, delay time.Duration) *RetryMiddleware {
	return &RetryMiddleware{
		maxRetries: maxRetries,
		delay:      delay,
		attempts:   make(map[string]int),
	}
}

// Handle retries failed dispatches
func (m *RetryMiddleware) Handle(event interface{}, next func(interface{}) error) error {
	eventName := getEventNameFromEvent(event)
	var err error

	for i := 0; i <= m.maxRetries; i++ {
		// Recover from panics
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v", r)
				}
			}()
			err = next(event)
		}()

		if err == nil {
			return nil
		}

		m.mu.Lock()
		m.attempts[eventName]++
		m.mu.Unlock()

		if i < m.maxRetries {
			time.Sleep(m.delay)
		}
	}

	return fmt.Errorf("event dispatch failed after %d retries: %w", m.maxRetries, err)
}

// GetAttempts returns the number of attempts for an event
func (m *RetryMiddleware) GetAttempts(eventName string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.attempts[eventName]
}

// Helper function to get event name
func getEventNameFromEvent(event interface{}) string {
	if e, ok := event.(Event); ok {
		return e.Name()
	}
	return fmt.Sprintf("%T", event)
}
