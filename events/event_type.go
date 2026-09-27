package events

import "reflect"

// EventType is a Listen key that selects events by their Go type instead of
// by name. Build one with OfType; the zero value selects nothing, and
// passing it to Listen panics with a *contract.RegistrationError.
type EventType struct {
	typeName string
	matches  func(event interface{}) bool
}

// OfType returns the Listen key for events of type T.
//
// For a concrete T the listener receives every event whose dynamic type is
// exactly T. Framework events are dispatched as pointers, so the key names
// the pointer type: OfType[*queue.JobFailed](). For an interface T the
// listener receives every event that implements it, which subscribes to a
// group of events at once: OfType[contract.FailureEvent]() receives every
// failure event the framework reports to the error handler.
//
// FakeDispatcher's assertions accept the same key. HasListeners and
// GetListeners take an event, not a key.
func OfType[T any]() EventType {
	return EventType{
		typeName: reflect.TypeFor[T]().String(),
		matches: func(event interface{}) bool {
			_, ok := event.(T)
			return ok
		},
	}
}
