package contract

import (
	"context"
	"time"
)

// EventListener handles events when they are dispatched. Implementations receive
// the caller-supplied context as the first argument so deadlines, trace IDs,
// and tx scopes flow through to listener bodies; listeners that block on I/O
// should honor ctx.Done().
type EventListener interface {
	// Handle processes the event
	Handle(ctx context.Context, event interface{}) error

	// ShouldQueue determines if this listener should be queued
	ShouldQueue() bool
}

// EventSubscriber registers multiple event listeners
type EventSubscriber interface {
	// Subscribe registers the subscriber's listeners
	Subscribe(dispatcher Dispatcher)
}

// Dispatcher manages event dispatching to listeners. All dispatch methods
// accept a context.Context so request-scoped values (transactions, trace IDs,
// deadlines) propagate to every listener.
type Dispatcher interface {
	// Listen registers a listener for one or more events and returns a listener ID.
	// The ID can be used with Off() to unregister the specific listener.
	Listen(events interface{}, listener EventListener) int

	// Off removes a listener by its ID.
	// Returns true if the listener was found and removed, false otherwise.
	Off(id int) bool

	// Subscribe registers an event subscriber
	Subscribe(subscriber EventSubscriber)

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
	GetListeners(event interface{}) []EventListener
}

// FailureEvent is implemented by events that represent a terminal failure
// which should reach the exception Reporter chain in addition to event
// listeners. The dispatcher bridges such events to ExceptionHandler.Report
// synchronously at dispatch time, so reporting stays reliable even when
// listener delivery is asynchronous or best-effort.
//
// Implement this ONLY on events whose error has no live caller observing
// it (background work: failed queue jobs, scheduled tasks, async
// listeners). Events whose error is returned to calling code (failed
// queries, outbound HTTP, request handler errors) must NOT implement it:
// the caller's boundary owns reporting, and bridging them would
// double-report. router.RequestFailed is deliberately excluded for this
// reason; request errors reach Report through the exceptions handler.
type FailureEvent interface {
	// FailureError returns the failure as an error for reporting.
	FailureError() error
}
