package events

import (
	"time"
)

// Event represents an event that can be dispatched
type Event interface {
	// Name returns the event name for identification
	Name() string
}

// Listener handles events when they are dispatched
type Listener interface {
	// Handle processes the event
	Handle(event interface{}) error

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

// Dispatcher manages event dispatching to listeners
type Dispatcher interface {
	// Listen registers a listener for one or more events
	Listen(events interface{}, listener Listener)

	// Subscribe registers an event subscriber
	Subscribe(subscriber Subscriber)

	// Dispatch fires an event to all registered listeners
	Dispatch(event interface{}) error

	// DispatchNow fires an event synchronously
	DispatchNow(event interface{}) error

	// DispatchAsync fires an event asynchronously
	DispatchAsync(event interface{}) error

	// DispatchAfter fires an event after a delay
	DispatchAfter(event interface{}, delay time.Duration) error

	// Until dispatches events until the first non-nil return
	Until(event interface{}) (interface{}, error)

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

// Observer handles model lifecycle events
type Observer interface {
	// Creating is called before a model is created
	Creating(model interface{}) error

	// Created is called after a model is created
	Created(model interface{})

	// Updating is called before a model is updated
	Updating(model interface{}) error

	// Updated is called after a model is updated
	Updated(model interface{})

	// Saving is called before a model is saved
	Saving(model interface{}) error

	// Saved is called after a model is saved
	Saved(model interface{})

	// Deleting is called before a model is deleted
	Deleting(model interface{}) error

	// Deleted is called after a model is deleted
	Deleted(model interface{})

	// Restoring is called before a soft-deleted model is restored
	Restoring(model interface{}) error

	// Restored is called after a soft-deleted model is restored
	Restored(model interface{})
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
func (l *BaseListener) Handle(event interface{}) error {
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
