package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/async"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// AsyncDispatcher handles asynchronous event dispatching
type AsyncDispatcher struct {
	// failureSinkMu guards failureSink so the sink can be reconfigured
	// concurrently with Push() without races.
	failureSinkMu sync.RWMutex
	failureSink   func(event interface{}) error
}

// AsyncFailed is dispatched when a listener invoked through the async
// dispatcher panics or returns an error. Applications can Listen("events.async_failed")
// to observe async failures (e.g. for alerting or metrics).
type AsyncFailed struct {
	Context      context.Context
	EventName    string
	ListenerName string
	Error        string
}

// Name returns the event name.
func (e *AsyncFailed) Name() string { return "events.async_failed" }

// FailureError implements contract.FailureEvent: a listener that panicked
// or errored on an async goroutine has no caller observing the failure, so
// the dispatcher bridges it to the exception Reporter chain.
func (e *AsyncFailed) FailureError() error {
	if e.Error == "" {
		return nil
	}
	return errors.New(e.Error)
}

// NewAsyncDispatcher creates a new async dispatcher
func NewAsyncDispatcher() *AsyncDispatcher {
	return &AsyncDispatcher{}
}

// SetFailureSink installs a sink that receives AsyncFailed events whenever a
// listener panics or returns an error from a goroutine spawned by Push().
// Passing nil disables failure dispatch.
func (a *AsyncDispatcher) SetFailureSink(fn func(event interface{}) error) {
	a.failureSinkMu.Lock()
	defer a.failureSinkMu.Unlock()
	a.failureSink = fn
}

// Push processes an event asynchronously in a panic-safe goroutine. Panics
// from the listener are recovered and surfaced as an events.async_failed event
// via the configured failure sink.
//
// Context semantics: the ctx passed to the listener is derived from the
// caller's ctx via context.WithoutCancel. Request-scoped values (trace IDs,
// tenant IDs, etc.) flow through, but cancellation and deadlines do NOT.
// The spawned goroutine can and will outlive the caller. Callers that need
// cancellation to propagate to the listener should not use Push; either run
// the listener synchronously or push to a queue with a cancellation contract
// of your own.
func (a *AsyncDispatcher) Push(ctx context.Context, event interface{}, listener Listener, delay time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	bgCtx := context.WithoutCancel(ctx)
	run := func() {
		defer func() {
			if p := recover(); p != nil {
				a.dispatchFailure(bgCtx, event, listener, panicerr.FromRecovered(p))
			}
		}()
		if err := listener.Handle(bgCtx, event); err != nil {
			a.dispatchFailure(bgCtx, event, listener, err)
		}
	}

	if delay > 0 {
		time.AfterFunc(delay, func() { async.Go(run) })
	} else {
		async.Go(run)
	}
	return nil
}

func (a *AsyncDispatcher) dispatchFailure(ctx context.Context, event interface{}, listener Listener, err error) {
	a.failureSinkMu.RLock()
	sink := a.failureSink
	a.failureSinkMu.RUnlock()
	if sink == nil || err == nil {
		return
	}
	// Best-effort: failure sink errors are intentionally ignored to avoid
	// runaway recursion on a misbehaving sink.
	_ = sink(&AsyncFailed{
		Context:      ctx,
		EventName:    resolveEventName(event),
		ListenerName: fmt.Sprintf("%T", listener),
		Error:        err.Error(),
	})
}

// EventJob represents a queued event job
type EventJob struct {
	Event        interface{}       `json:"event"`
	ListenerType string            `json:"listener_type"`
	Timestamp    time.Time         `json:"timestamp"`
	Attempts     int               `json:"attempts"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// EventWorker processes queued events
type EventWorker struct {
	dispatcher Dispatcher
	// mu guards the listeners map. Without this, concurrent RegisterListener
	// and Process calls race on the map and trigger a fatal runtime panic
	// ("concurrent map read and map write"). The factory itself is invoked
	// outside the lock so listener Handle() calls cannot deadlock with
	// registration.
	mu        sync.RWMutex
	listeners map[string]func() Listener // Factory functions for listeners
}

// NewEventWorker creates a new event worker
func NewEventWorker(dispatcher Dispatcher) *EventWorker {
	return &EventWorker{
		dispatcher: dispatcher,
		listeners:  make(map[string]func() Listener),
	}
}

// RegisterListener registers a listener factory for async processing
func (w *EventWorker) RegisterListener(listenerType string, factory func() Listener) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.listeners[listenerType] = factory
}

// Process processes a queued event job
func (w *EventWorker) Process(ctx context.Context, jobData string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Unmarshal the job
	var job EventJob
	if err := json.Unmarshal([]byte(jobData), &job); err != nil {
		return fmt.Errorf("failed to unmarshal event job: %w", err)
	}

	// Get the listener factory under read lock, then release before calling
	// the factory or the listener. Holding the lock across user code would
	// risk deadlocks (e.g. a listener that registers another listener) and
	// block unrelated registrations for the duration of Handle().
	w.mu.RLock()
	factory, ok := w.listeners[job.ListenerType]
	w.mu.RUnlock()
	if !ok {
		return fmt.Errorf("velocity/events: unknown listener type %s: %w", job.ListenerType, ErrListenerNotFound)
	}

	// Create the listener instance
	listener := factory()

	// Handle the event
	return listener.Handle(ctx, job.Event)
}

// AsyncEventBus combines sync and async dispatching
type AsyncEventBus struct {
	*DefaultDispatcher
	asyncDispatcher *AsyncDispatcher
	worker          *EventWorker
}

// NewAsyncEventBus creates a new async event bus
func NewAsyncEventBus() *AsyncEventBus {
	dispatcher := NewDispatcher()
	asyncDispatcher := NewAsyncDispatcher()
	worker := NewEventWorker(dispatcher)

	// Set the queue dispatcher
	dispatcher.SetQueueDispatcher(asyncDispatcher)

	return &AsyncEventBus{
		DefaultDispatcher: dispatcher,
		asyncDispatcher:   asyncDispatcher,
		worker:            worker,
	}
}

// RegisterQueuedListener registers a queued listener with its factory
func (b *AsyncEventBus) RegisterQueuedListener(event string, listener QueuedListener, factory func() Listener) {
	// Register with dispatcher
	b.Listen(event, listener)

	// Register factory for async processing
	listenerType := fmt.Sprintf("%T", listener)
	b.worker.RegisterListener(listenerType, factory)
}

// ProcessQueuedEvent processes a queued event (called by queue workers)
func (b *AsyncEventBus) ProcessQueuedEvent(ctx context.Context, jobData string) error {
	return b.worker.Process(ctx, jobData)
}

// PendingEvents tracks events that should be dispatched after database commit
type PendingEvents struct {
	events []interface{}
	mu     sync.RWMutex
}

// NewPendingEvents creates a new pending events tracker
func NewPendingEvents() *PendingEvents {
	return &PendingEvents{
		events: make([]interface{}, 0),
	}
}

// Add adds an event to pending
func (p *PendingEvents) Add(event interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
}

// Flush returns and clears all pending events
func (p *PendingEvents) Flush() []interface{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	events := p.events
	p.events = make([]interface{}, 0)
	return events
}

// Clear clears all pending events without returning them
func (p *PendingEvents) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = make([]interface{}, 0)
}

// TransactionalDispatcher wraps a dispatcher with transaction support
type TransactionalDispatcher struct {
	Dispatcher
	pending       *PendingEvents
	inTransaction bool
	mu            sync.RWMutex
}

// NewTransactionalDispatcher creates a new transactional dispatcher
func NewTransactionalDispatcher(dispatcher Dispatcher) *TransactionalDispatcher {
	return &TransactionalDispatcher{
		Dispatcher: dispatcher,
		pending:    NewPendingEvents(),
	}
}

// BeginTransaction marks the start of a transaction
func (t *TransactionalDispatcher) BeginTransaction() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inTransaction = true
}

// Commit commits the transaction and dispatches pending events
func (t *TransactionalDispatcher) Commit(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	t.inTransaction = false
	t.mu.Unlock()

	// Dispatch all pending events
	for _, event := range t.pending.Flush() {
		if err := t.Dispatcher.Dispatch(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// Rollback rolls back the transaction and clears pending events
func (t *TransactionalDispatcher) Rollback() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inTransaction = false
	t.pending.Clear()
}

// DispatchAfterCommit dispatches an event after the current transaction
// commits. When invoked inside a tx, the event is queued and only fires on
// Commit; when invoked outside a tx, the event is dispatched immediately
// and any error from the underlying dispatcher is returned to the caller.
//
// Returning the error matters: previously this method swallowed dispatcher
// failures on the non-tx branch, which made the contract silently weaker
// outside a tx than inside one. Callers that explicitly want fire-and-
// forget semantics should wrap the call with `_ = ...`.
func (t *TransactionalDispatcher) DispatchAfterCommit(ctx context.Context, event interface{}) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.RLock()
	inTx := t.inTransaction
	t.mu.RUnlock()

	if inTx {
		t.pending.Add(event)
		return nil
	}
	// Not in transaction, dispatch immediately. Surface the error so
	// the caller can react instead of silently swallowing it.
	return t.Dispatcher.Dispatch(ctx, event)
}

// Conformance: AsyncFailed participates in the failure-report bridge.
var _ contract.FailureEvent = (*AsyncFailed)(nil)
