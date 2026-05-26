package events

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// EventListenerJob implements the queue.Job interface for event listeners
type EventListenerJob struct {
	Event        interface{} `json:"event"`
	EventType    string      `json:"event_type"`
	ListenerType string      `json:"listener_type"`
	Attempts     int         `json:"attempts"`
	MaxRetries   int         `json:"max_retries"`
	listener     Listener    `json:"-"` // Not serialized, set when processing
}

// Handle processes the event listener job. Implements queue.Job.
func (j *EventListenerJob) Handle() error {
	return j.HandleCtx(context.Background())
}

// HandleCtx processes the event listener job with the worker-supplied ctx.
// Implements queue.HandleCtxer so the listener receives the worker context.
func (j *EventListenerJob) HandleCtx(ctx context.Context) error {
	if j.listener == nil {
		return fmt.Errorf("listener not set for job")
	}
	return j.listener.Handle(ctx, j.Event)
}

// Failed handles job failure
func (j *EventListenerJob) Failed(err error) {
	// Log the failure - in a real implementation this might send alerts
	// or store failure information for analysis
}

// QueueIntegratedDispatcher extends DefaultDispatcher with deep queue integration.
//
// The qmu mutex guards queueDriver and listenerRegistry. We use a dedicated
// lock on the outer struct rather than reusing the embedded *DefaultDispatcher.mu
// so the listener-map invariants (Listen / Off / Dispatch path) stay isolated
// from the queue-integration invariants (SetQueueDriver / RegisterListenerFactory
// / pushToQueue / ProcessEventListenerJob). Sharing one lock across both surfaces
// would force the dispatch fast path to contend with infrequent queue-driver
// reconfiguration, and would make reasoning about lock ordering harder if either
// surface ever grew nested calls.
type QueueIntegratedDispatcher struct {
	*DefaultDispatcher
	qmu              sync.RWMutex
	listenerRegistry map[string]func() Listener // Registry of listener factories
	queueDriver      queue.Driver               // Injected queue driver
}

// NewQueueIntegratedDispatcher creates a new queue-integrated dispatcher
func NewQueueIntegratedDispatcher() *QueueIntegratedDispatcher {
	return &QueueIntegratedDispatcher{
		DefaultDispatcher: NewDispatcher(),
		listenerRegistry:  make(map[string]func() Listener),
	}
}

// SetQueueDriver sets the queue driver for dispatching queued listeners.
// Safe to call concurrently with pushToQueue / Dispatch.
func (d *QueueIntegratedDispatcher) SetQueueDriver(driver queue.Driver) {
	d.qmu.Lock()
	defer d.qmu.Unlock()
	d.queueDriver = driver
}

// RegisterListenerFactory registers a factory function for creating listener instances.
// Safe to call concurrently with ProcessEventListenerJob and other registrations.
func (d *QueueIntegratedDispatcher) RegisterListenerFactory(listenerType string, factory func() Listener) {
	d.qmu.Lock()
	d.listenerRegistry[listenerType] = factory
	d.qmu.Unlock()
}

// Dispatch fires an event to all registered listeners with enhanced queue support
func (d *QueueIntegratedDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		if listener.ShouldQueue() {
			// Enhanced queue integration
			if err := d.pushToQueue(ctx, event, listener); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
		} else {
			// Process synchronously
			if err := d.processListener(ctx, event, listener); err != nil {
				return err
			}
		}
	}

	return nil
}

// pushToQueue pushes a listener to the queue with proper event serialization
func (d *QueueIntegratedDispatcher) pushToQueue(ctx context.Context, event interface{}, listener Listener) error {
	// Create the job
	job := &EventListenerJob{
		Event:        event,
		EventType:    d.getEventName(event),
		ListenerType: d.getListenerType(listener),
		Attempts:     0,
		MaxRetries:   3, // Default
	}

	// Set max retries if listener supports it
	if ql, ok := listener.(QueuedListener); ok {
		job.MaxRetries = ql.Tries()
	}

	// Get queue configuration
	queueName := "default"
	var delay time.Duration

	if ql, ok := listener.(QueuedListener); ok {
		if q := ql.OnQueue(); q != "" {
			queueName = q
		}
		delay = ql.WithDelay()
	}

	// Snapshot the queue driver under qmu so a concurrent SetQueueDriver
	// cannot race the read. The PushCtx call itself runs lock-free because
	// drivers may block on IO and we do not want SetQueueDriver to wait
	// behind a slow push.
	d.qmu.RLock()
	driver := d.queueDriver
	d.qmu.RUnlock()
	if driver == nil {
		return fmt.Errorf("queue driver not set on QueueIntegratedDispatcher")
	}
	if delay > 0 {
		return driver.PushDelayedCtx(ctx, job, delay, queueName)
	}
	return driver.PushCtx(ctx, job, queueName)
}

// getListenerType returns a string representation of the listener type
func (d *QueueIntegratedDispatcher) getListenerType(listener Listener) string {
	t := reflect.TypeOf(listener)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t.String()
}

// ProcessEventListenerJob processes an event listener job from the queue
func (d *QueueIntegratedDispatcher) ProcessEventListenerJob(ctx context.Context, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var job EventListenerJob
	if err := json.Unmarshal(data, &job); err != nil {
		return fmt.Errorf("failed to unmarshal event listener job: %w", err)
	}

	// Read the factory under qmu so a concurrent RegisterListenerFactory
	// cannot race the map read. The factory call itself runs lock-free
	// because listener construction may allocate or do work the dispatcher
	// should not block other registrations on.
	d.qmu.RLock()
	factory, ok := d.listenerRegistry[job.ListenerType]
	d.qmu.RUnlock()
	if !ok {
		return fmt.Errorf("velocity/events: no factory registered for listener type %s: %w", job.ListenerType, ErrListenerNotFound)
	}

	// Create listener instance
	job.listener = factory()

	// Process the job
	return job.HandleCtx(ctx)
}

// EventJobFactory creates EventListenerJob instances for queue deserialization
func EventJobFactory(data []byte) (queue.Job, error) {
	var job EventListenerJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event listener job: %w", err)
	}
	return &job, nil
}

// Initialize queue integration by registering the event job type. Uses
// the generic RegisterJob form so the registry key is derived from the
// concrete job type, eliminating the typo footgun the deprecated string-keyed
// queue.Register had.
func InitializeQueueIntegration() {
	queue.RegisterJob(func(data []byte) (*EventListenerJob, error) {
		var job EventListenerJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event listener job: %w", err)
		}
		return &job, nil
	})
}

// PriorityListener extends Listener with priority support
type PriorityListener interface {
	Listener
	// Priority returns the listener priority (higher numbers = higher priority)
	Priority() int
}

// PriorityDispatcher handles listeners with priority ordering
type PriorityDispatcher struct {
	*QueueIntegratedDispatcher
}

// NewPriorityDispatcher creates a new priority-aware dispatcher
func NewPriorityDispatcher() *PriorityDispatcher {
	return &PriorityDispatcher{
		QueueIntegratedDispatcher: NewQueueIntegratedDispatcher(),
	}
}

// getListenersForEvent retrieves all listeners for an event, sorted by priority
func (d *PriorityDispatcher) getListenersForEvent(event interface{}) []Listener {
	listeners := d.QueueIntegratedDispatcher.getListenersForEvent(event)

	// Sort by priority (higher priority first)
	for i := 0; i < len(listeners)-1; i++ {
		for j := i + 1; j < len(listeners); j++ {
			pi, ok1 := listeners[i].(PriorityListener)
			pj, ok2 := listeners[j].(PriorityListener)

			// If both have priority, sort by priority
			if ok1 && ok2 {
				if pj.Priority() > pi.Priority() {
					listeners[i], listeners[j] = listeners[j], listeners[i]
				}
			} else if ok2 && !ok1 {
				// Priority listeners come before non-priority
				listeners[i], listeners[j] = listeners[j], listeners[i]
			}
		}
	}

	return listeners
}

// StoppableEvent allows events to signal that propagation should stop
type StoppableEvent interface {
	Event
	// ShouldStopPropagation returns true if event propagation should stop
	ShouldStopPropagation() bool
	// StopPropagation marks the event to stop propagation
	StopPropagation()
}

// BaseStoppableEvent provides a base implementation of StoppableEvent
type BaseStoppableEvent struct {
	BaseEvent
	stopped bool
}

// ShouldStopPropagation returns whether propagation should stop
func (e *BaseStoppableEvent) ShouldStopPropagation() bool {
	return e.stopped
}

// StopPropagation stops event propagation
func (e *BaseStoppableEvent) StopPropagation() {
	e.stopped = true
}

// StoppablePropagationDispatcher handles events that can stop propagation
type StoppablePropagationDispatcher struct {
	*PriorityDispatcher
}

// NewStoppablePropagationDispatcher creates a new stoppable propagation dispatcher
func NewStoppablePropagationDispatcher() *StoppablePropagationDispatcher {
	return &StoppablePropagationDispatcher{
		PriorityDispatcher: NewPriorityDispatcher(),
	}
}

// Dispatch fires an event with support for stopping propagation
func (d *StoppablePropagationDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	listeners := d.getListenersForEvent(event)

	for _, listener := range listeners {
		// Check if we should stop propagation
		if stoppable, ok := event.(StoppableEvent); ok {
			if stoppable.ShouldStopPropagation() {
				break
			}
		}

		if listener.ShouldQueue() {
			// For queued listeners, we don't stop propagation since they're async
			if err := d.pushToQueue(ctx, event, listener); err != nil {
				return fmt.Errorf("failed to queue listener: %w", err)
			}
		} else {
			// Process synchronously
			if err := d.processListener(ctx, event, listener); err != nil {
				return err
			}
		}
	}

	return nil
}

// StoppablePropagationListener can signal to stop event propagation
type StoppablePropagationListener interface {
	Listener
	// HandleWithPropagation processes the event and can stop propagation
	HandleWithPropagation(ctx context.Context, event interface{}) (stopPropagation bool, err error)
}

// processListener executes a listener with enhanced propagation control
func (d *StoppablePropagationDispatcher) processListener(ctx context.Context, event interface{}, listener Listener) error {
	// Check if listener should handle this event
	if handler, ok := listener.(ShouldHandle); ok {
		if !handler.ShouldHandle(event) {
			return nil
		}
	}

	// Check if listener can control propagation
	if propagationListener, ok := listener.(StoppablePropagationListener); ok {
		stopPropagation, err := propagationListener.HandleWithPropagation(ctx, event)
		if err != nil {
			return err
		}

		// If listener wants to stop propagation and event supports it
		if stopPropagation {
			if stoppable, ok := event.(StoppableEvent); ok {
				stoppable.StopPropagation()
			}
		}
		return nil
	}

	// Regular listener handling
	return listener.Handle(ctx, event)
}
