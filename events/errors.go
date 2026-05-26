package events

import "errors"

var (
	ErrListenerNotFound  = errors.New("velocity/events: listener not found")
	ErrDispatcherStopped = errors.New("velocity/events: dispatcher stopped")
	// ErrEventTypeNotRegistered is returned by the queue-hydration path
	// when an event arrives off the wire whose EventType has no
	// registered factory. Without a factory, json.Unmarshal would
	// produce map[string]any and any listener typed against the original
	// struct would receive the wrong value -- the typed-event hole the
	// H-22 follow-up closes. Producers refuse to enqueue when this
	// applies; consumers route the error through FailureReporter.
	ErrEventTypeNotRegistered = errors.New("velocity/events: event type not registered")
)
