package events

import (
	"github.com/velocitykode/velocity/pkg/cache"
	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/router"
)

// wirePackageHooks sets up event dispatching for all framework packages.
// This is called when the events package is initialized.
func wirePackageHooks() {
	dispatch := func(event interface{}) error {
		return Dispatch(event)
	}

	router.SetEventDispatcher(dispatch)
	orm.SetEventDispatcher(dispatch)
	cache.SetEventDispatcher(dispatch)
}

// clearPackageHooks removes event dispatchers from all packages.
// This is called when resetting the events system (e.g., for testing).
func clearPackageHooks() {
	router.SetEventDispatcher(nil)
	orm.SetEventDispatcher(nil)
	cache.SetEventDispatcher(nil)
}

// ListenerFunc is a function that handles events.
// This provides a simpler alternative to implementing the Listener interface.
type ListenerFunc func(event interface{}) error

// Handle implements the Listener interface for ListenerFunc.
func (f ListenerFunc) Handle(event interface{}) error {
	return f(event)
}

// ShouldQueue returns false - function listeners are always synchronous.
func (f ListenerFunc) ShouldQueue() bool {
	return false
}

// On registers a function handler for one or more events.
// This is a convenience method that wraps a function as a Listener.
//
// Example:
//
//	events.On("query.executed", func(e interface{}) error {
//	    q := e.(*orm.QueryExecuted)
//	    log.Printf("Query: %s took %v", q.SQL, q.Duration)
//	    return nil
//	})
func On(eventName interface{}, handler func(event interface{}) error) {
	Listen(eventName, ListenerFunc(handler))
}

// OnEvent is a generic helper for type-safe event handling.
// It extracts the event, type-asserts it, and calls your handler.
//
// Example:
//
//	events.On("query.executed", events.OnEvent(func(e *orm.QueryExecuted) error {
//	    log.Printf("Query: %s took %v", e.SQL, e.Duration)
//	    return nil
//	}))
func OnEvent[T any](handler func(event *T) error) func(interface{}) error {
	return func(event interface{}) error {
		if e, ok := event.(*T); ok {
			return handler(e)
		}
		return nil // Ignore events of wrong type
	}
}
