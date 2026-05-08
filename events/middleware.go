package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/pipeline"
)

// EventMiddleware processes events before they reach listeners. Implementations
// receive the caller-supplied ctx so deadlines and trace context propagate
// through the pipeline; passing ctx through to next() preserves the chain.
type EventMiddleware interface {
	// Handle processes the event and calls the next middleware
	Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error
}

// MiddlewareFunc adapts a function to the EventMiddleware interface
type MiddlewareFunc func(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error

// Handle implements EventMiddleware
func (f MiddlewareFunc) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	return f(ctx, event, next)
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
func (d *MiddlewareDispatcher) UseFunc(fn func(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error) {
	d.Use(MiddlewareFunc(fn))
}

// mwFrame bundles the request ctx with the event so the pipeline.Stage[T]
// shape (which carries a single T) can pass both through middleware.
type mwFrame struct {
	ctx   context.Context
	event interface{}
}

// Dispatch dispatches an event through middleware chain
func (d *MiddlewareDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	middlewares := make([]EventMiddleware, len(d.middlewares))
	copy(middlewares, d.middlewares)
	d.mu.RUnlock()

	pipes := make([]pipeline.Stage[mwFrame], len(middlewares))
	for i, mw := range middlewares {
		mw := mw
		pipes[i] = pipeline.Pipe[mwFrame](func(frame mwFrame, next func(mwFrame) error) error {
			return mw.Handle(frame.ctx, frame.event, func(c context.Context, e interface{}) error {
				return next(mwFrame{ctx: c, event: e})
			})
		})
	}

	return pipeline.New[mwFrame]().
		Send(mwFrame{ctx: ctx, event: event}).
		Through(pipes...).
		Then(func(f mwFrame) error {
			return d.DefaultDispatcher.Dispatch(f.ctx, f.event)
		})
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
func (m *LoggingMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	m.mu.Lock()
	eventName := getEventNameFromEvent(event)
	timestamp := time.Now().Format("15:04:05.000")
	m.log = append(m.log, fmt.Sprintf("[%s] Event dispatched: %s", timestamp, eventName))
	m.mu.Unlock()

	return next(ctx, event)
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
	validator func(context.Context, interface{}) error
}

// NewValidationMiddleware creates a new validation middleware
func NewValidationMiddleware(validator func(context.Context, interface{}) error) *ValidationMiddleware {
	return &ValidationMiddleware{
		validator: validator,
	}
}

// Handle validates the event
func (m *ValidationMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	if err := m.validator(ctx, event); err != nil {
		return fmt.Errorf("event validation failed: %w", err)
	}
	return next(ctx, event)
}

// TransformMiddleware transforms events before dispatching
type TransformMiddleware struct {
	transformer func(context.Context, interface{}) interface{}
}

// NewTransformMiddleware creates a new transform middleware
func NewTransformMiddleware(transformer func(context.Context, interface{}) interface{}) *TransformMiddleware {
	return &TransformMiddleware{
		transformer: transformer,
	}
}

// Handle transforms the event
func (m *TransformMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	transformed := m.transformer(ctx, event)
	return next(ctx, transformed)
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
func (m *FilterMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	if !m.condition(event) {
		return nil // Skip event
	}
	return next(ctx, event)
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
func (m *TimingMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	start := time.Now()
	err := next(ctx, event)
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
func (m *RetryMiddleware) Handle(ctx context.Context, event interface{}, next func(context.Context, interface{}) error) error {
	eventName := getEventNameFromEvent(event)
	var err error

	for i := 0; i <= m.maxRetries; i++ {
		// Recover from panics
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = panicerr.FromRecovered(r)
				}
			}()
			err = next(ctx, event)
		}()

		if err == nil {
			return nil
		}

		m.mu.Lock()
		m.attempts[eventName]++
		m.mu.Unlock()

		if i < m.maxRetries {
			// Honour ctx cancellation while sleeping for the next retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.delay):
			}
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

// getEventNameFromEvent extracts the event name from an event value.
func getEventNameFromEvent(event interface{}) string {
	return resolveEventName(event)
}
