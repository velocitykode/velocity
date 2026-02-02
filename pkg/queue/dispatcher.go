package queue

// eventDispatcher is called when queue events occur.
// This is set by the events package during initialization.
var eventDispatcher func(event interface{}) error

// SetEventDispatcher sets the function used to dispatch events.
// This is called by the events package to wire up event dispatching.
func SetEventDispatcher(fn func(event interface{}) error) {
	eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func dispatchEvent(event interface{}) {
	if eventDispatcher != nil {
		eventDispatcher(event)
	}
}
