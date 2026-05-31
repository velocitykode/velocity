package events

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// Event represents an event that can be dispatched
type Event interface {
	// Name returns the event name for identification
	Name() string
}

// Listener is an alias for the stdlib-only contract.EventListener interface.
// The canonical definition lives in the contract leaf.
type Listener = contract.EventListener

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

// Subscriber is an alias for the stdlib-only contract.EventSubscriber
// interface. The canonical definition lives in the contract leaf.
type Subscriber = contract.EventSubscriber

// Dispatcher is an alias for the stdlib-only contract.Dispatcher interface.
// The canonical definition lives in the contract leaf.
type Dispatcher = contract.Dispatcher

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
