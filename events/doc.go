// Package events implements the framework's event dispatcher with
// synchronous, asynchronous, and queue-backed listener execution.
//
// # Optional listener capabilities
//
// Listeners MAY implement any of these to opt into framework features:
//
//	QueueableListener     ShouldQueue() returns true so the dispatcher
//	                      routes the event onto the queue instead of
//	                      invoking the listener inline.
//
//	PriorityListener      Priority() returns an integer; the
//	                      PriorityDispatcher executes higher values
//	                      first. Listeners without a Priority sort
//	                      below those with one (priority 0).
//
//	StoppablePropagationListener
//	                      HandleStoppable(ctx, event) returns
//	                      (stopped bool, err error); a true return halts
//	                      further propagation when used with
//	                      StoppablePropagationDispatcher.
//
// # Optional event capabilities
//
//	StoppableEvent        ShouldStopPropagation() / StopPropagation()
//	                      let an event signal mid-dispatch that no
//	                      further listeners should run. Embed
//	                      BaseStoppableEvent to satisfy the interface
//	                      without writing the bookkeeping.
//
// Capability detection is a plain type assertion at the call site
// (dispatcher loops check listener/event for the optional interface
// before invoking the capability-specific path).
package events
