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

// EventListenerJob implements the queue.Job interface for event listeners.
//
// A queued listener is enqueued with the listener TYPE NAME (ListenerType)
// rather than a live pointer. The queue worker rehydrates the job from JSON
// bytes via the package job registry; the worker's process may not be the
// dispatcher's process, so the listener instance must be reconstructable
// from the type name alone using the package-level listener factory
// registry (RegisterListenerFactory / lookupListenerFactory).
type EventListenerJob struct {
	Event        interface{} `json:"event"`
	EventType    string      `json:"event_type"`
	ListenerType string      `json:"listener_type"`
	Attempts     int         `json:"attempts"`
	MaxRetries   int         `json:"max_retries"`
	listener     Listener    `json:"-"` // Not serialized; populated on hydration or by tests.
}

// Handle processes the event listener job. Implements queue.Job.
func (j *EventListenerJob) Handle() error {
	return j.HandleCtx(context.Background())
}

// HandleCtx processes the event listener job with the worker-supplied ctx.
// Implements queue.HandleCtxer so the listener receives the worker context.
//
// If j.listener is nil (the normal cross-process worker path, where the job
// was rehydrated from JSON bytes), the listener is reconstructed via the
// package-level listener factory registry keyed on ListenerType. A missing
// registration is a real error and surfaces to the worker so it can route
// the job to failed_jobs / events -- silently dropping a queued security
// listener was the H-22 hole.
func (j *EventListenerJob) HandleCtx(ctx context.Context) error {
	if j.listener == nil {
		factory, ok := lookupListenerFactory(j.ListenerType)
		if !ok {
			return fmt.Errorf("velocity/events: no factory registered for listener type %q: %w", j.ListenerType, ErrListenerNotFound)
		}
		j.listener = factory()
		if j.listener == nil {
			return fmt.Errorf("velocity/events: listener factory for %q returned nil", j.ListenerType)
		}
	}
	return j.listener.Handle(ctx, j.Event)
}

// Failed is invoked by the queue driver once the job has exhausted its retry
// budget. The previous no-op silently dropped queued security / audit
// listeners. Route the error through the package-level failure reporter so
// the framework's exceptions handler (or any other reporter installed via
// SetFailureReporter) records the drop. When no reporter is installed --
// e.g. in tests that exercise the queue path standalone -- the call becomes
// a documented no-op rather than a silent one (it is still observable via
// the test's assertion on the original Handle error).
func (j *EventListenerJob) Failed(err error) {
	reportFailure(j, err)
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

// RegisterListenerFactory registers a factory function for creating listener
// instances. The factory is written to both the per-dispatcher map (for the
// in-process ProcessEventListenerJob path) and the package-level registry
// (so a different process running the queue worker can rehydrate the
// listener from the persisted ListenerType -- this is the H-22 fix).
// Safe to call concurrently with ProcessEventListenerJob and other
// registrations.
func (d *QueueIntegratedDispatcher) RegisterListenerFactory(listenerType string, factory func() Listener) {
	d.qmu.Lock()
	d.listenerRegistry[listenerType] = factory
	d.qmu.Unlock()
	RegisterListenerFactory(listenerType, factory)
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

// pushToQueue pushes a listener to the queue with proper event serialization.
// Refuses to enqueue when no listener factory has been registered for the
// listener's type, because the worker would not be able to rehydrate the
// listener and the job would silently move to failed_jobs (H-22). Returning
// an error here surfaces the misconfiguration on the dispatch side, where
// it can be fixed at boot.
func (d *QueueIntegratedDispatcher) pushToQueue(ctx context.Context, event interface{}, listener Listener) error {
	listenerType := d.getListenerType(listener)
	if _, ok := lookupListenerFactory(listenerType); !ok {
		return fmt.Errorf("velocity/events: refusing to enqueue listener %q: no factory registered (call RegisterListenerFactory before Dispatch): %w", listenerType, ErrListenerNotFound)
	}

	// Create the job
	job := &EventListenerJob{
		Event:        event,
		EventType:    d.getEventName(event),
		ListenerType: listenerType,
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

// EventJobFactory creates EventListenerJob instances for queue
// deserialization. The hydrated job intentionally has listener == nil; the
// listener is reconstructed inside EventListenerJob.HandleCtx via the
// package-level listener factory registry, because the worker process may
// not share producer-side memory.
func EventJobFactory(data []byte) (queue.Job, error) {
	var job EventListenerJob
	if err := json.Unmarshal(data, &job); err != nil {
		return nil, fmt.Errorf("failed to unmarshal event listener job: %w", err)
	}
	return &job, nil
}

// FailureReporter receives queued-listener failure callbacks invoked by
// queue.Driver.Failed once the job has exhausted its retry budget. The
// framework wires the App's exceptions handler via InitializeQueueIntegration
// so a silently dropped security / audit listener becomes visible to the
// configured reporters (sentry, log, etc).
type FailureReporter func(job *EventListenerJob, err error)

// InitializeQueueIntegration wires the queue-integration plumbing that turns
// queued listeners from a silent-drop hole (H-22) into a production-ready
// path. It is idempotent: repeated calls overwrite the previous wiring with
// the new arguments, so consumers can re-invoke it during hot config reloads.
//
// Arguments:
//   - dispatcher: the QueueIntegratedDispatcher to bind to the queue driver.
//     May be nil when consumers manage the dispatcher separately.
//   - driver: the queue.Driver that pushed jobs land on. May be nil when
//     consumers only want to register the job factory and reporter.
//   - reporter: optional callback that fires from EventListenerJob.Failed.
//     Nil disables the reporter (calls become no-ops); pass a closure over
//     exceptions.Handler.Report to route to the framework's exception sink.
//
// The function also registers the EventListenerJob with the queue's typed
// job registry (queue.RegisterJob) so cross-process workers can rehydrate
// jobs from JSON bytes. The registry key is derived from the job type, so
// producer and consumer paths stay symmetric by construction.
func InitializeQueueIntegration(dispatcher *QueueIntegratedDispatcher, driver queue.Driver, reporter FailureReporter) {
	queue.RegisterJob(func(data []byte) (*EventListenerJob, error) {
		var job EventListenerJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event listener job: %w", err)
		}
		return &job, nil
	})

	if dispatcher != nil && driver != nil {
		dispatcher.SetQueueDriver(driver)
	}

	setFailureReporter(reporter)
}

// --- package-level listener factory registry (H-22) ---------------------

var (
	listenerFactoryMu sync.RWMutex
	listenerFactories = make(map[string]func() Listener)

	failureReporterMu sync.RWMutex
	failureReporter   FailureReporter
)

// RegisterListenerFactory registers a factory function at the package level
// for the listener type name. The package-level registry lets queue workers
// running in a different process than the producer rehydrate a listener
// instance from the persisted ListenerType -- this is the H-22 fix.
// Multiple calls with the same name overwrite the previous factory.
// Safe to call concurrently.
func RegisterListenerFactory(listenerType string, factory func() Listener) {
	if listenerType == "" || factory == nil {
		return
	}
	listenerFactoryMu.Lock()
	listenerFactories[listenerType] = factory
	listenerFactoryMu.Unlock()
}

// UnregisterListenerFactory removes a previously registered factory. Mainly
// useful in tests; production code rarely needs to unregister.
func UnregisterListenerFactory(listenerType string) {
	listenerFactoryMu.Lock()
	delete(listenerFactories, listenerType)
	listenerFactoryMu.Unlock()
}

// lookupListenerFactory returns the registered factory for a listener type
// name. The bool is false when no factory is registered; callers MUST treat
// that as an error rather than substituting a stub (the previous "listener
// not set" no-op trap that H-22 closed).
func lookupListenerFactory(listenerType string) (func() Listener, bool) {
	listenerFactoryMu.RLock()
	factory, ok := listenerFactories[listenerType]
	listenerFactoryMu.RUnlock()
	return factory, ok
}

// setFailureReporter installs the package-level FailureReporter invoked by
// EventListenerJob.Failed. Nil clears the reporter (subsequent Failed calls
// become explicit no-ops). Safe to call concurrently with Failed.
func setFailureReporter(fn FailureReporter) {
	failureReporterMu.Lock()
	failureReporter = fn
	failureReporterMu.Unlock()
}

// reportFailure routes a queued-listener failure through the installed
// reporter, recovering from any panic in the reporter so a misbehaving
// sink cannot take down the queue worker.
func reportFailure(job *EventListenerJob, err error) {
	failureReporterMu.RLock()
	fn := failureReporter
	failureReporterMu.RUnlock()
	if fn == nil || err == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	fn(job, err)
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
