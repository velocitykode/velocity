package events

import (
	"context"
	"time"
)

// Event represents an event that can be dispatched
type Event interface {
	// Name returns the event name for identification
	Name() string
}

// Listener handles events when they are dispatched. Implementations receive
// the caller-supplied context as the first argument so deadlines, trace IDs,
// and tx scopes flow through to listener bodies; listeners that block on I/O
// should honor ctx.Done().
type Listener interface {
	// Handle processes the event
	Handle(ctx context.Context, event interface{}) error

	// ShouldQueue determines if this listener should be queued
	ShouldQueue() bool
}

// QueuedListener extends Listener with queue configuration
type QueuedListener interface {
	Listener

	// OnConnection specifies the queue connection
	OnConnection() string

	// OnQueue specifies the queue name
	OnQueue() string

	// WithDelay specifies the delay before processing
	WithDelay() time.Duration

	// Tries specifies the number of retry attempts
	Tries() int
}

// Subscriber registers multiple event listeners
type Subscriber interface {
	// Subscribe registers the subscriber's listeners
	Subscribe(dispatcher Dispatcher)
}

// Dispatcher manages event dispatching to listeners. All dispatch methods
// accept a context.Context so request-scoped values (transactions, trace IDs,
// deadlines) propagate to every listener.
type Dispatcher interface {
	// Listen registers a listener for one or more events and returns a listener ID.
	// The ID can be used with Off() to unregister the specific listener.
	Listen(events interface{}, listener Listener) int

	// Off removes a listener by its ID.
	// Returns true if the listener was found and removed, false otherwise.
	Off(id int) bool

	// Subscribe registers an event subscriber
	Subscribe(subscriber Subscriber)

	// Dispatch fires an event to all registered listeners
	Dispatch(ctx context.Context, event interface{}) error

	// DispatchNow fires an event synchronously
	DispatchNow(ctx context.Context, event interface{}) error

	// DispatchAsync fires an event asynchronously.
	//
	// Context semantics: the ctx passed to listeners is derived from the
	// caller's ctx via context.WithoutCancel. This means request-scoped
	// values (trace IDs, tenant IDs, anything stored via context.WithValue)
	// flow through to listeners, but the caller's cancellation and deadline
	// do NOT. A listener invoked through DispatchAsync may outlive the
	// caller. For example, an async listener will keep running after the
	// originating HTTP request has returned to the client.
	//
	// Callers who need cancellation to propagate (e.g. background workers
	// that must abort when their parent ctx is cancelled) should not use
	// DispatchAsync. Use Dispatch instead and have the listener perform its
	// own backgrounding (e.g. spawn a goroutine that observes ctx.Done()),
	// or push a job onto the queue directly with the cancellation contract
	// you want.
	DispatchAsync(ctx context.Context, event interface{}) error

	// DispatchAfter fires an event after a delay.
	//
	// Context semantics: the ctx passed to listeners is derived from the
	// caller's ctx via context.WithoutCancel. Request-scoped values (trace
	// IDs, tenant IDs, anything stored via context.WithValue) flow through
	// to listeners, but the caller's cancellation and deadline do NOT. The
	// listener fires after delay has elapsed and will routinely outlive
	// the caller. For example, a 30s delayed listener kicked off from an
	// HTTP request will keep running long after the response has flushed.
	//
	// Callers who need cancellation to propagate (e.g. background workers
	// that must abort when their parent ctx is cancelled) should not use
	// DispatchAfter. Use Dispatch instead and have the listener perform its
	// own backgrounding (e.g. spawn a goroutine that observes ctx.Done()),
	// or push a job onto the queue directly with the cancellation contract
	// you want.
	DispatchAfter(ctx context.Context, event interface{}, delay time.Duration) error

	// Until dispatches events until the first non-nil return
	Until(ctx context.Context, event interface{}) (interface{}, error)

	// Flush removes all listeners for an event
	Flush(event string)

	// Forget removes specific listeners
	Forget(event string)

	// HasListeners checks if an event has listeners
	HasListeners(event interface{}) bool

	// GetListeners returns all listeners for an event
	GetListeners(event interface{}) []Listener
}

// Broadcastable represents an event that should be broadcast
type Broadcastable interface {
	Event

	// ShouldBroadcast determines if the event should be broadcast
	ShouldBroadcast() bool

	// BroadcastOn returns the channels to broadcast on
	BroadcastOn() []string

	// BroadcastAs returns the event name for broadcasting
	BroadcastAs() string

	// BroadcastWith returns the data to broadcast
	BroadcastWith() map[string]interface{}

	// BroadcastWhen returns conditions for broadcasting
	BroadcastWhen() bool
}

// ShouldHandle allows conditional event handling
type ShouldHandle interface {
	// ShouldHandle determines if the listener should handle the event
	ShouldHandle(event interface{}) bool
}

// Observable represents a model that can be observed
type Observable interface {
	// Observe registers an observer for the model
	Observe(observer Observer)

	// GetObservers returns all registered observers
	GetObservers() []Observer
}

// Observer handles model lifecycle events. Every callback receives the
// caller-supplied context.Context so deadlines and tx scopes flow through to
// observer bodies. All methods return error for consistency with ModelObserver.
type Observer interface {
	// Creating is called before a model is created
	Creating(ctx context.Context, model interface{}) error

	// Created is called after a model is created
	Created(ctx context.Context, model interface{}) error

	// Updating is called before a model is updated
	Updating(ctx context.Context, model interface{}) error

	// Updated is called after a model is updated
	Updated(ctx context.Context, model interface{}) error

	// Saving is called before a model is saved
	Saving(ctx context.Context, model interface{}) error

	// Saved is called after a model is saved
	Saved(ctx context.Context, model interface{}) error

	// Deleting is called before a model is deleted
	Deleting(ctx context.Context, model interface{}) error

	// Deleted is called after a model is deleted
	Deleted(ctx context.Context, model interface{}) error

	// Restoring is called before a soft-deleted model is restored
	Restoring(ctx context.Context, model interface{}) error

	// Restored is called after a soft-deleted model is restored
	Restored(ctx context.Context, model interface{}) error
}

// BaseEvent provides a base implementation of Event
type BaseEvent struct {
	EventName string
}

// Name returns the event name
func (e *BaseEvent) Name() string {
	if e.EventName != "" {
		return e.EventName
	}
	return "base.event"
}

// BaseListener provides a base implementation of Listener
type BaseListener struct{}

// Handle processes the event (override in implementations)
func (l *BaseListener) Handle(ctx context.Context, event interface{}) error {
	return nil
}

// ShouldQueue returns whether the listener should be queued
func (l *BaseListener) ShouldQueue() bool {
	return false
}

// QueuedBaseListener provides a base for queued listeners
type QueuedBaseListener struct {
	BaseListener
	Connection string
	Queue      string
	Delay      time.Duration
	MaxTries   int
}

// OnConnection returns the queue connection
func (l *QueuedBaseListener) OnConnection() string {
	if l.Connection != "" {
		return l.Connection
	}
	return "default"
}

// OnQueue returns the queue name
func (l *QueuedBaseListener) OnQueue() string {
	if l.Queue != "" {
		return l.Queue
	}
	return "default"
}

// WithDelay returns the processing delay
func (l *QueuedBaseListener) WithDelay() time.Duration {
	return l.Delay
}

// Tries returns the number of retry attempts
func (l *QueuedBaseListener) Tries() int {
	if l.MaxTries > 0 {
		return l.MaxTries
	}
	return 3
}

// ShouldQueue returns true for queued listeners
func (l *QueuedBaseListener) ShouldQueue() bool {
	return true
}
