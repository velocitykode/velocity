package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// AsyncFailed is dispatched when a listener fails with no caller waiting on
// it: a listener the no-queue fallback of DispatchAsync or DispatchAfter
// ran returned an error or panicked. The dispatcher dispatches one per
// failed listener, and the failure-report bridge reports each to the error
// Reporter chain. Applications can listen for it (for example for alerting
// or metrics).
type AsyncFailed struct {
	// EventMeta's Context is the context the listener ran under: the
	// caller's, detached from its cancellation, so the envelope carries the
	// caller's trace ids.
	contract.EventMeta
	// EventName is the name of the event the listener was handling.
	EventName string
	// ListenerName is the listener's Go type.
	ListenerName string
	// Err is the failure itself: the listener's error, or the recovered
	// panic as an error. Its JSON form is its text.
	Err error
}

// Name returns the event name.
func (e *AsyncFailed) Name() string { return "events.listener.failed" }

// MarshalJSON encodes the event with Err as its text.
func (e AsyncFailed) MarshalJSON() ([]byte, error) {
	type fields AsyncFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *AsyncFailed) UnmarshalJSON(data []byte) error {
	type fields AsyncFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// FailureError implements contract.FailureEvent: a listener that failed with
// no caller waiting on it has no caller observing the failure, so the
// dispatcher bridges it to the error Reporter chain. It returns Err, the
// failure with its type (for an event decoded from its JSON form, an error
// with its text), or nil when there is none.
func (e *AsyncFailed) FailureError() error {
	return e.Err
}

// FailureSource implements contract.FailureEvent: the failure is a
// listener's.
func (e *AsyncFailed) FailureSource() contract.ErrorSource {
	return contract.ErrorSourceListener
}

// newAsyncFailed returns the AsyncFailed for listener, which failed with
// err while handling the event named eventName under ctx. The name is the
// one the event's listeners were resolved by: the event's Name is user
// code, and calling it again could fail or answer differently.
func newAsyncFailed(ctx context.Context, eventName string, listener Listener, err error) *AsyncFailed {
	return &AsyncFailed{
		EventMeta:    eventmeta.Current(ctx),
		EventName:    eventName,
		ListenerName: fmt.Sprintf("%T", listener),
		Err:          err,
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
