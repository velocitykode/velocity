package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// jsonNullLiteral is the canonical wire form of a JSON null. A
// json.RawMessage(nil) field marshals as `null` rather than being omitted,
// so the consumer side must treat both forms as "no payload" when deciding
// whether to invoke the event factory.
var jsonNullLiteral = []byte("null")

// EventListenerJob implements the queue.Job interface for event listeners.
//
// Wire format: Event is the JSON-encoded event payload (json.RawMessage so
// the bytes survive a Marshal -> Unmarshal round-trip through the queue
// without losing concrete typing). EventType is the producer-side
// reflect.TypeOf(event).String() identifier used to look up the factory
// registered via RegisterEventFactory on the consumer side; without this
// pairing the worker would unmarshal Event back into map[string]any and
// any listener typed against the original struct would receive the wrong
// value (the issue the type-preservation follow-up closes).
//
// A queued listener is enqueued with the listener TYPE NAME (ListenerType)
// rather than a live pointer. The queue worker rehydrates the job from JSON
// bytes via the package job registry; the worker's process may not be the
// dispatcher's process, so the listener instance must be reconstructable
// from the type name alone using the package-level listener factory
// registry (RegisterListenerFactory / lookupListenerFactory).
//
// In-process fast path: the unexported event field carries the original
// live event pointer alongside the marshalled bytes so the memory queue
// driver, which retains the live job pointer across a same-process Pop,
// can skip the JSON round-trip entirely on HandleCtx.
type EventListenerJob struct {
	// Event is the JSON-encoded event payload that survives cross-process
	// hydration. Type is json.RawMessage rather than interface{} so the
	// payload bytes round-trip unchanged through the queue driver and the
	// consumer side is free to unmarshal them into the concrete type
	// supplied by the event factory.
	Event        json.RawMessage `json:"event"`
	EventType    string          `json:"event_type"`
	ListenerType string          `json:"listener_type"`
	Attempts     int             `json:"attempts"`
	MaxRetries   int             `json:"max_retries"`

	// listener is the live consumer-side listener instance. Populated on
	// hydration in HandleCtx via the listener factory registry, or by
	// tests that exercise Handle directly.
	listener Listener `json:"-"`
	// event is the producer-side live event pointer retained for the
	// in-process fast path (memory driver same-process Pop). Cross-process
	// pops always have event == nil and HandleCtx reconstructs the value
	// from Event + EventType via the event factory registry.
	event interface{} `json:"-"`
}

// Handle processes the event listener job. Implements queue.Job.
func (j *EventListenerJob) Handle() error {
	return j.HandleCtx(context.Background())
}

// HandleCtx processes the event listener job with the worker-supplied ctx.
// Implements queue.HandleCtxer so the listener receives the worker context.
//
// Hydration:
//   - j.listener: populated from the listener factory registry keyed on
//     ListenerType when nil. A missing registration is a real error and
//     surfaces to the worker so it can route the job to failed_jobs /
//     events. Silently dropping a queued security listener was the H-22
//     hole; substituting a stub here would reopen it.
//   - j.event: returned directly when the producer stashed the live
//     pointer (in-process memory driver). When nil (cross-process worker
//     path), the event is reconstructed via the event factory registry
//     keyed on EventType and Event raw bytes are unmarshalled into the
//     fresh concrete value. Without this step json.Unmarshal would
//     produce map[string]any and a listener typed against the original
//     struct would never see its fields.
func (j *EventListenerJob) HandleCtx(ctx context.Context) error {
	if j.listener == nil {
		lfactory, ok := lookupListenerFactory(j.ListenerType)
		if !ok {
			return fmt.Errorf("velocity/events: no factory registered for listener type %q: %w", j.ListenerType, ErrListenerNotFound)
		}
		j.listener = lfactory()
		if j.listener == nil {
			return fmt.Errorf("velocity/events: listener factory for %q returned nil", j.ListenerType)
		}
	}

	payload, err := j.hydrateEvent()
	if err != nil {
		return err
	}
	return j.listener.Handle(ctx, payload)
}

// hydrateEvent returns the concrete event value the listener should
// receive. The in-process producer-side pointer is preferred when set;
// otherwise the event is reconstructed from EventType + Event raw bytes.
//
// Lookup order:
//  1. Built-in scalar shortcut (newScalarEventValue): the named Go scalar
//     types (string, bool, int / int64 / float64, []byte, json.RawMessage)
//     are hydrated without consulting the user-supplied event factory
//     registry. Scalars carry no ambiguity at unmarshal time (no nested
//     fields can come back as map[string]any), so requiring every app
//     that dispatches a string-named event through a queued listener to
//     register a factory was a compatibility regression.
//  2. User-registered factory via lookupEventFactory. Concrete struct
//     events still require explicit registration so the cross-process
//     typing guarantee is preserved.
//
// A missing factory for a non-scalar type is a real error (mirrors the
// listener-factory contract) so the failure can be reported and routed
// to failed_jobs rather than silently handed map[string]any.
func (j *EventListenerJob) hydrateEvent() (interface{}, error) {
	if j.event != nil {
		return j.event, nil
	}
	// Allow legacy / hand-constructed jobs that carry no payload bytes
	// (e.g. tests that only assert Failed routing) to fall through with
	// a nil event. json.RawMessage(nil) round-trips through Marshal as
	// the literal `null`, so both forms collapse to a no-op hydrate.
	if len(j.Event) == 0 || bytes.Equal(j.Event, jsonNullLiteral) {
		return nil, nil
	}
	if j.EventType == "" {
		return nil, fmt.Errorf("velocity/events: event payload without event_type: %w", ErrEventTypeNotRegistered)
	}

	// Built-in scalar shortcut: no user registration required.
	if value, ok := newScalarEventValue(j.EventType); ok {
		if err := json.Unmarshal(j.Event, value); err != nil {
			return nil, fmt.Errorf("velocity/events: failed to unmarshal scalar event payload into %q: %w", j.EventType, err)
		}
		return derefScalarValue(value, j.EventType), nil
	}

	efactory, ok := lookupEventFactory(j.EventType)
	if !ok {
		return nil, fmt.Errorf("velocity/events: no factory registered for event type %q: %w", j.EventType, ErrEventTypeNotRegistered)
	}
	value := efactory()
	if value == nil {
		return nil, fmt.Errorf("velocity/events: event factory for %q returned nil", j.EventType)
	}
	if err := json.Unmarshal(j.Event, value); err != nil {
		return nil, fmt.Errorf("velocity/events: failed to unmarshal event payload into %q: %w", j.EventType, err)
	}
	return value, nil
}

// MaxAttempts reports the job's retry budget so the queue worker honours the
// QueuedListener's Tries() value. The worker derives a job's budget solely
// from the queue.MaxAttempter interface; without this method every queued
// listener silently fell back to the worker's package default regardless of
// Tries(). MaxRetries is populated by pushToQueue from QueuedListener.Tries()
// (default 3) and round-trips through JSON (json:"max_retries"), so a job
// hydrated in a different worker process carries the same budget.
//
// Guard: a non-positive MaxRetries (legacy or hand-built payloads that never
// set the field) returns the package default of 3 rather than 0, which the
// worker would read as "no retries allowed" and fail the job on first error.
// This preserves the pre-fix behaviour for those payloads.
func (j *EventListenerJob) MaxAttempts() int {
	if j.MaxRetries <= 0 {
		return 3
	}
	return j.MaxRetries
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

	// listenersFor resolves the ordered listener slice Dispatch iterates.
	// It MUST be used instead of calling d.getListenersForEvent directly
	// from Dispatch: Go binds method calls on the embedded *DefaultDispatcher
	// statically, so a subtype that overrides getListenersForEvent (e.g.
	// PriorityDispatcher) could never have its override observed through the
	// inherited Dispatch method -- the call inside Dispatch always sees the
	// *QueueIntegratedDispatcher receiver and the promoted default. This hook
	// gives subtypes a virtual-dispatch seam: NewQueueIntegratedDispatcher
	// defaults it to the promoted default, NewPriorityDispatcher rebinds it
	// to the priority-sorting variant.
	listenersFor func(event interface{}) []Listener
}

// NewQueueIntegratedDispatcher creates a new queue-integrated dispatcher
func NewQueueIntegratedDispatcher() *QueueIntegratedDispatcher {
	d := &QueueIntegratedDispatcher{
		DefaultDispatcher: NewDispatcher(),
		listenerRegistry:  make(map[string]func() Listener),
	}
	d.listenersFor = d.getListenersForEvent
	return d
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

// Dispatch fires an event to all registered listeners with enhanced queue support.
//
// This override of the embedded DefaultDispatcher.Dispatch must keep the
// failure-reporter bridge: SetFailureReporter is inherited from the embedded
// dispatcher, so bootstrap installs the hook on this type too, and a
// FailureEvent dispatched here has to reach the Reporter chain exactly like
// one dispatched through DefaultDispatcher.
//
// Three behaviours are aligned with DefaultDispatcher.Dispatch:
//   - Listener resolution goes through the listenersFor hook so a subtype's
//     getListenersForEvent override (PriorityDispatcher's priority sort) is
//     honoured. Calling d.getListenersForEvent directly would bind to the
//     promoted default and silently ignore the override.
//   - A listener that opts into ShouldDispatchAfterCommit is deferred onto
//     the after-commit queue via EnqueueAfterCommit and replayed at commit
//     (through pushToQueue when it also ShouldQueue, otherwise inline),
//     mirroring dispatcher.go. Outside a transaction the gate collapses to
//     the inline / queue branches.
//   - Listener errors are aggregated with errors.Join rather than returned
//     first-wins, so one failing listener does not mask the rest.
func (d *QueueIntegratedDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = d.reportFailure(ctx, event)
	listeners := d.listenersFor(event)

	var errs []error
	for _, listener := range listeners {
		// After-commit gate runs FIRST: a listener that opts into
		// post-commit delivery must not reach the queue or inline branch
		// while the transaction is still in flight. EnqueueAfterCommit
		// returns false when no queue is installed (or it already drained),
		// collapsing the gate into the branches below.
		if ac, ok := listener.(ShouldDispatchAfterCommit); ok && ac.ShouldDispatchAfterCommit() {
			ev := event
			ln := listener
			if EnqueueAfterCommit(ctx, func(replayCtx context.Context) error {
				if ln.ShouldQueue() {
					if err := d.pushToQueue(replayCtx, ev, ln); err != nil {
						return fmt.Errorf("failed to queue listener: %w", err)
					}
					return nil
				}
				return d.processListener(replayCtx, ev, ln)
			}) {
				continue
			}
			// Fall through: no queue installed or already drained; the
			// listener fires inline / via queue just like a non-opt-in one.
		}

		if listener.ShouldQueue() {
			// Enhanced queue integration
			if err := d.pushToQueue(ctx, event, listener); err != nil {
				errs = append(errs, fmt.Errorf("failed to queue listener: %w", err))
			}
		} else {
			// Process synchronously
			if err := d.processListener(ctx, event, listener); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

// pushToQueue pushes a listener to the queue with proper event serialization.
// Refuses to enqueue when either the listener-type or the event-type lacks
// a registered factory: the worker would otherwise hydrate the job, fail to
// rebuild the concrete listener or event, and either silently drop the work
// (the H-22 hole) or invoke the listener with map[string]any (the typed-
// event hole the follow-up closes). Both errors surface on the dispatch
// side so a misconfiguration is caught at boot, not when the audit-log
// listener silently fails on the worker.
func (d *QueueIntegratedDispatcher) pushToQueue(ctx context.Context, event interface{}, listener Listener) error {
	listenerType := d.getListenerType(listener)
	if _, ok := lookupListenerFactory(listenerType); !ok {
		return fmt.Errorf("velocity/events: refusing to enqueue listener %q: no factory registered (call RegisterListenerFactory before Dispatch): %w", listenerType, ErrListenerNotFound)
	}

	eventType := eventTypeKey(event)
	if !isScalarEventType(eventType) {
		if _, ok := lookupEventFactory(eventType); !ok {
			return fmt.Errorf("velocity/events: refusing to enqueue event %q for listener %q: no event factory registered (call RegisterEventFactory before Dispatch): %w", eventType, listenerType, ErrEventTypeNotRegistered)
		}
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("velocity/events: failed to marshal event %q: %w", eventType, err)
	}

	// Create the job. The live event pointer is stashed on the unexported
	// `event` field for the in-process fast path; cross-process workers
	// see j.event == nil after json.Unmarshal and reconstruct the value
	// from Event + EventType via the registry.
	job := &EventListenerJob{
		Event:        payload,
		EventType:    eventType,
		ListenerType: listenerType,
		Attempts:     0,
		MaxRetries:   3, // Default
		event:        event,
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

// eventTypeKey returns the stable registry key for an event value. The key
// is reflect.TypeOf(event).String() so producer and consumer sides agree on
// pointer vs value forms by construction: a producer that dispatches a
// pointer (the convention, matching the factory signature) emits a key
// like "*pkg.UserSignedUp"; a value-form dispatch emits "pkg.UserSignedUp".
// Mixing the two forms across registration and dispatch is a registration
// error; the dispatch-side refusal in pushToQueue surfaces it.
//
// String events (events dispatched as the raw event-name string used by the
// listener-routing tables, e.g. d.Dispatch(ctx, "user.created")) collapse
// to "string". The built-in scalar shortcut (newScalarEventValue) lets the
// hydration path skip the user registry for the named Go scalar types, so
// apps that dispatch through queued listeners with a bare string payload
// keep working without a RegisterEventFactory call.
func eventTypeKey(event interface{}) string {
	if event == nil {
		return ""
	}
	return reflect.TypeOf(event).String()
}

// isScalarEventType reports whether the EventType key is one of the
// built-in Go scalar payload types the hydration path can rebuild without
// consulting the user-supplied event factory registry. These types have no
// ambiguity at json.Unmarshal time (no nested fields can come back as
// map[string]any), so the cross-process typing guarantee for struct events
// is not weakened by exempting them.
func isScalarEventType(typeName string) bool {
	switch typeName {
	case "string",
		"bool",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64",
		"[]uint8",         // []byte
		"json.RawMessage": // alias for []byte but reflect emits this distinct name
		return true
	}
	return false
}

// newScalarEventValue returns a fresh pointer-to-zero of the named scalar
// type so json.Unmarshal can populate it, plus true. When typeName is not a
// recognised scalar, returns (nil, false) and the caller must consult the
// user registry. The returned pointer is always non-nil so the unmarshal
// call site does not need to nil-check.
func newScalarEventValue(typeName string) (interface{}, bool) {
	switch typeName {
	case "string":
		return new(string), true
	case "bool":
		return new(bool), true
	case "int":
		return new(int), true
	case "int8":
		return new(int8), true
	case "int16":
		return new(int16), true
	case "int32":
		return new(int32), true
	case "int64":
		return new(int64), true
	case "uint":
		return new(uint), true
	case "uint8":
		return new(uint8), true
	case "uint16":
		return new(uint16), true
	case "uint32":
		return new(uint32), true
	case "uint64":
		return new(uint64), true
	case "float32":
		return new(float32), true
	case "float64":
		return new(float64), true
	case "[]uint8":
		return new([]byte), true
	case "json.RawMessage":
		return new(json.RawMessage), true
	}
	return nil, false
}

// derefScalarValue unwraps the *T pointer newScalarEventValue produced so
// the listener observes the same value form the producer dispatched: a
// dispatcher that calls Dispatch(ctx, "user.created") expects the listener
// to receive "user.created" (string), not *string. For pointer-shaped
// scalars the producer dispatched directly (rare; e.g. *json.RawMessage)
// callers stay on the pointer form.
func derefScalarValue(p interface{}, typeName string) interface{} {
	switch typeName {
	case "string":
		return *(p.(*string))
	case "bool":
		return *(p.(*bool))
	case "int":
		return *(p.(*int))
	case "int8":
		return *(p.(*int8))
	case "int16":
		return *(p.(*int16))
	case "int32":
		return *(p.(*int32))
	case "int64":
		return *(p.(*int64))
	case "uint":
		return *(p.(*uint))
	case "uint8":
		return *(p.(*uint8))
	case "uint16":
		return *(p.(*uint16))
	case "uint32":
		return *(p.(*uint32))
	case "uint64":
		return *(p.(*uint64))
	case "float32":
		return *(p.(*float32))
	case "float64":
		return *(p.(*float64))
	case "[]uint8":
		return *(p.(*[]byte))
	case "json.RawMessage":
		return *(p.(*json.RawMessage))
	}
	return p
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
//
// Deprecated: this is a bootstrap-only hook wired exactly once from
// velocity.App.bootstrap. The canonical entry point now lives at
// github.com/velocitykode/velocity/internal/eventqueue.InitializeQueueIntegration
// so that consumer code cannot import it. The shim here is retained so
// the white-box tests in this package keep compiling without an import
// cycle; new framework callers MUST import the internal package.
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

	// eventFactoryMu / eventFactories form the event-type registry that
	// preserves concrete event typing across cross-process queue
	// hydration. Without it the worker would unmarshal the persisted
	// json.RawMessage payload into map[string]any and any listener typed
	// against the original struct would receive the wrong value.
	eventFactoryMu sync.RWMutex
	eventFactories = make(map[string]func() interface{})

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

// RegisterEventFactory registers a factory for an event type name. The key
// must match reflect.TypeOf(event).String() of the value the producer
// dispatches; the convention is to register a pointer-returning factory
// because that mirrors the form the producer dispatches and the form
// json.Unmarshal requires:
//
//	type UserSignedUp struct { ID int; Email string }
//	events.RegisterEventFactory("*pkg.UserSignedUp", func() interface{} {
//	    return &UserSignedUp{}
//	})
//
// Mismatched value-vs-pointer forms between dispatch and registration are
// caught synchronously by the dispatch-side refusal in pushToQueue: the
// dispatcher uses the live reflect type to look up the factory, so the
// keys must agree. Multiple calls with the same name overwrite the
// previous factory. Safe to call concurrently.
//
// The factory's interface{} return type avoids forcing every event to
// implement the events.Event interface (BaseEvent / value structs without
// Name() are valid event payloads). When an event does implement Event,
// the factory can still return it -- the dispatcher does not call Name()
// on the hydrated value.
func RegisterEventFactory(typeName string, factory func() interface{}) {
	if typeName == "" || factory == nil {
		return
	}
	eventFactoryMu.Lock()
	eventFactories[typeName] = factory
	eventFactoryMu.Unlock()
}

// UnregisterEventFactory removes a previously registered event factory.
// Mainly useful in tests; production code rarely needs to unregister.
func UnregisterEventFactory(typeName string) {
	eventFactoryMu.Lock()
	delete(eventFactories, typeName)
	eventFactoryMu.Unlock()
}

// lookupEventFactory returns the registered factory for an event type name.
// The bool is false when no factory is registered; HandleCtx callers MUST
// treat that as an error rather than falling through to a generic
// map[string]any (the typed-event hole this follow-up closes).
func lookupEventFactory(typeName string) (func() interface{}, bool) {
	eventFactoryMu.RLock()
	factory, ok := eventFactories[typeName]
	eventFactoryMu.RUnlock()
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
	d := &PriorityDispatcher{
		QueueIntegratedDispatcher: NewQueueIntegratedDispatcher(),
	}
	// Rebind the embedded dispatcher's listener-resolution hook to the
	// priority-sorting override so the inherited Dispatch observes it (Go
	// would otherwise bind statically to the promoted default).
	d.QueueIntegratedDispatcher.listenersFor = d.getListenersForEvent
	return d
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

// Dispatch fires an event with support for stopping propagation.
//
// Listener resolution here is already correct without the listenersFor hook:
// this is the subtype's OWN Dispatch, so d.getListenersForEvent binds to the
// promoted PriorityDispatcher override under the *StoppablePropagationDispatcher
// receiver. The after-commit gate and errors.Join aggregation are aligned with
// DefaultDispatcher.Dispatch; the StoppableEvent break is preserved (checked
// before each listener so a prior synchronous listener that called
// StopPropagation halts the chain).
func (d *StoppablePropagationDispatcher) Dispatch(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	listeners := d.getListenersForEvent(event)

	var errs []error
	for _, listener := range listeners {
		// Check if we should stop propagation
		if stoppable, ok := event.(StoppableEvent); ok {
			if stoppable.ShouldStopPropagation() {
				break
			}
		}

		// After-commit gate runs before the queue / inline branches, exactly
		// as in QueueIntegratedDispatcher.Dispatch. The replay closure uses
		// this type's processListener (propagation-aware) for the inline path.
		if ac, ok := listener.(ShouldDispatchAfterCommit); ok && ac.ShouldDispatchAfterCommit() {
			ev := event
			ln := listener
			if EnqueueAfterCommit(ctx, func(replayCtx context.Context) error {
				// Re-check propagation at replay time so an earlier after-commit
				// StoppablePropagationListener that called StopPropagation halts
				// the already-enqueued later listeners, matching the inline path.
				if stoppable, ok := ev.(StoppableEvent); ok && stoppable.ShouldStopPropagation() {
					return nil
				}
				if ln.ShouldQueue() {
					if err := d.pushToQueue(replayCtx, ev, ln); err != nil {
						return fmt.Errorf("failed to queue listener: %w", err)
					}
					return nil
				}
				return d.processListener(replayCtx, ev, ln)
			}) {
				continue
			}
		}

		if listener.ShouldQueue() {
			// For queued listeners, we don't stop propagation since they're async
			if err := d.pushToQueue(ctx, event, listener); err != nil {
				errs = append(errs, fmt.Errorf("failed to queue listener: %w", err))
			}
		} else {
			// Process synchronously
			if err := d.processListener(ctx, event, listener); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
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
