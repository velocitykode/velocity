package events

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// AsyncFailed is dispatched when a listener fails with no caller waiting on
// it: a listener the no-queue fallback of DispatchAsync or DispatchAfter
// ran returned an error or panicked. The dispatcher dispatches one per
// failed listener, and the failure-report bridge reports each to the error
// Reporter chain. Applications can listen for it (for example for alerting
// or metrics).
type AsyncFailed struct {
	// Context is the context the listener ran under: the caller's,
	// detached from its cancellation, so it carries the caller's trace IDs.
	Context context.Context
	// EventName is the name of the event the listener was handling.
	EventName string
	// ListenerName is the listener's Go type.
	ListenerName string
	// Error is the failure's text.
	Error string
	// Err is the failure itself: the listener's error, or the recovered
	// panic as an error. It is not serialized: the JSON form keeps Error
	// alone.
	Err error `json:"-"`
	// TraceID, SpanID and ParentID are the trace IDs of Context.
	TraceID  string
	SpanID   string
	ParentID string
}

// Name returns the event name.
func (e *AsyncFailed) Name() string { return "events.listener.failed" }

// FailureError implements contract.FailureEvent: a listener that failed with
// no caller waiting on it has no caller observing the failure, so the
// dispatcher bridges it to the error Reporter chain. It returns Err, the
// failure with its type; an event without Err (one decoded from its JSON
// form) returns a new error with the Error text, or nil when there is none.
func (e *AsyncFailed) FailureError() error {
	if e.Err != nil {
		return e.Err
	}
	if e.Error == "" {
		return nil
	}
	return errors.New(e.Error)
}

// FailureSource implements contract.FailureEvent: the failure is a
// listener's.
func (e *AsyncFailed) FailureSource() contract.ErrorSource {
	return contract.ErrorSourceListener
}

// newAsyncFailed returns the AsyncFailed for listener, which failed with
// err while handling event under ctx.
func newAsyncFailed(ctx context.Context, event interface{}, listener Listener, err error) *AsyncFailed {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	return &AsyncFailed{
		Context:      ctx,
		EventName:    resolveEventName(event),
		ListenerName: fmt.Sprintf("%T", listener),
		Error:        err.Error(),
		Err:          err,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	}
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

// BeginTransaction marks the start of a transaction.
//
// inTransaction is a single per-instance flag shared across all goroutines
// using this dispatcher: there is one flag for all concurrent transactions,
// not one per goroutine or per logical transaction. A TransactionalDispatcher
// therefore models a single ambient transaction scope, not isolated
// concurrent transactions; callers needing per-goroutine isolation should
// use the ctx-scoped BufferedDispatcher instead.
func (t *TransactionalDispatcher) BeginTransaction() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inTransaction = true
}

// Commit commits the transaction and dispatches pending events.
//
// Partial-failure semantics: pending events are drained up front, then
// dispatched in order. If a dispatch fails at index i, the failing event
// plus every event after it are re-added to the pending buffer (the failed
// event is included to match BufferedDispatcher's retry contract) and the
// error is returned. Commit may therefore be retried: a subsequent Commit
// replays the remaining events from the failure point.
func (t *TransactionalDispatcher) Commit(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	t.mu.Lock()
	t.inTransaction = false
	t.mu.Unlock()

	// Dispatch all pending events
	events := t.pending.Flush()
	for i, event := range events {
		if err := t.Dispatcher.Dispatch(ctx, event); err != nil {
			// Re-add the failing event and the remainder so a retry Commit
			// can replay them; successful events (0..i-1) already fired.
			for _, remaining := range events[i:] {
				t.pending.Add(remaining)
			}
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
