package events

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// AsyncDispatcher handles asynchronous event dispatching
type AsyncDispatcher struct {
	// For now, we'll use goroutines instead of a queue system
	// This can be replaced with actual queue integration later
}

// NewAsyncDispatcher creates a new async dispatcher
func NewAsyncDispatcher() *AsyncDispatcher {
	return &AsyncDispatcher{}
}

// Push processes an event asynchronously.
// Listener panics are recovered so one misbehaving listener does not
// tear down the process.
func (a *AsyncDispatcher) Push(event interface{}, listener Listener, delay time.Duration) error {
	safeHandle := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("velocity/events: async listener panic recovered: %v", panicerr.FromRecovered(r))
			}
		}()
		_ = listener.Handle(event)
	}
	if delay > 0 {
		time.AfterFunc(delay, safeHandle)
	} else {
		go safeHandle()
	}
	return nil
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
	listeners  map[string]func() Listener // Factory functions for listeners
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
	w.listeners[listenerType] = factory
}

// Process processes a queued event job
func (w *EventWorker) Process(jobData string) error {
	// Unmarshal the job
	var job EventJob
	if err := json.Unmarshal([]byte(jobData), &job); err != nil {
		return fmt.Errorf("failed to unmarshal event job: %w", err)
	}

	// Get the listener factory
	factory, ok := w.listeners[job.ListenerType]
	if !ok {
		return fmt.Errorf("velocity/events: unknown listener type %s: %w", job.ListenerType, ErrListenerNotFound)
	}

	// Create the listener instance
	listener := factory()

	// Handle the event
	return listener.Handle(job.Event)
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
func (b *AsyncEventBus) ProcessQueuedEvent(jobData string) error {
	return b.worker.Process(jobData)
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
func (t *TransactionalDispatcher) Commit() error {
	t.mu.Lock()
	t.inTransaction = false
	t.mu.Unlock()

	// Dispatch all pending events
	for _, event := range t.pending.Flush() {
		if err := t.Dispatcher.Dispatch(event); err != nil {
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

// DispatchAfterCommit dispatches an event after the current transaction commits
func (t *TransactionalDispatcher) DispatchAfterCommit(event interface{}) {
	t.mu.RLock()
	inTx := t.inTransaction
	t.mu.RUnlock()

	if inTx {
		t.pending.Add(event)
	} else {
		// Not in transaction, dispatch immediately
		t.Dispatcher.Dispatch(event)
	}
}
