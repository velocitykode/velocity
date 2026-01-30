package events

import (
	"sync"
	"time"
)

var (
	globalDispatcher Dispatcher
	globalMu         sync.RWMutex
	once             sync.Once
)

// Initialize sets up the global event dispatcher
func Initialize(dispatcher Dispatcher) {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalDispatcher = dispatcher
	// Re-wire the package hooks with the new dispatcher
	wirePackageHooks()
}

// GetDispatcher returns the global event dispatcher
func GetDispatcher() Dispatcher {
	once.Do(func() {
		if globalDispatcher == nil {
			// Create default dispatcher if not initialized
			globalDispatcher = NewDispatcher()
		}
		// Wire up event dispatching for router, orm, cache packages
		wirePackageHooks()
	})

	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalDispatcher
}

// Listen registers a listener for one or more events using the global dispatcher.
// Returns a listener ID that can be used with Off() to unregister the listener.
func Listen(events interface{}, listener Listener) int {
	return GetDispatcher().Listen(events, listener)
}

// Off removes a listener by its ID using the global dispatcher.
// Returns true if the listener was found and removed, false otherwise.
func Off(id int) bool {
	return GetDispatcher().Off(id)
}

// Subscribe registers an event subscriber using the global dispatcher
func Subscribe(subscriber Subscriber) {
	GetDispatcher().Subscribe(subscriber)
}

// Dispatch fires an event using the global dispatcher
func Dispatch(event interface{}) error {
	return GetDispatcher().Dispatch(event)
}

// DispatchNow fires an event synchronously using the global dispatcher
func DispatchNow(event interface{}) error {
	return GetDispatcher().DispatchNow(event)
}

// DispatchAsync fires an event asynchronously using the global dispatcher
func DispatchAsync(event interface{}) error {
	return GetDispatcher().DispatchAsync(event)
}

// DispatchAfter fires an event after a delay using the global dispatcher
func DispatchAfter(event interface{}, delay time.Duration) error {
	return GetDispatcher().DispatchAfter(event, delay)
}

// Until dispatches events until the first non-nil return using the global dispatcher
func Until(event interface{}) (interface{}, error) {
	return GetDispatcher().Until(event)
}

// Flush removes all listeners for an event using the global dispatcher
func Flush(event string) {
	GetDispatcher().Flush(event)
}

// Forget removes specific listeners using the global dispatcher
func Forget(event string) {
	GetDispatcher().Forget(event)
}

// HasListeners checks if an event has listeners using the global dispatcher
func HasListeners(event interface{}) bool {
	return GetDispatcher().HasListeners(event)
}

// GetListeners returns all listeners for an event using the global dispatcher
func GetListeners(event interface{}) []Listener {
	return GetDispatcher().GetListeners(event)
}

// Reset resets the global dispatcher (useful for testing)
func Reset() {
	globalMu.Lock()
	defer globalMu.Unlock()
	globalDispatcher = nil
	once = sync.Once{}
	// Clear package hooks
	clearPackageHooks()
}
