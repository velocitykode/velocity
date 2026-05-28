// Package events implements the framework's event dispatcher with
// synchronous, asynchronous, and queue-backed listener execution.
//
// # Listener interface
//
// The core [Listener] interface has ShouldQueue baked in (it is not a
// separate capability). Returning true from ShouldQueue routes the event
// onto the configured queue driver via [QueueIntegratedDispatcher]
// instead of invoking the listener inline.
//
// # Optional listener capabilities
//
// Listeners MAY implement any of these in addition to the core Listener
// interface to opt into framework features:
//
//	QueuedListener              OnConnection / OnQueue / WithDelay /
//	                            Tries override default queue routing
//	                            and retry policy when the listener is
//	                            dispatched via the queue
//	                            (ShouldQueue() == true).
//
//	PriorityListener            Priority() returns an integer. The
//	                            PriorityDispatcher executes higher
//	                            values first; listeners without
//	                            Priority() sort below those with one.
//
//	StoppablePropagationListener
//	                            HandleWithPropagation(ctx, event)
//	                            returns (stopPropagation bool, err).
//	                            When dispatched via
//	                            StoppablePropagationDispatcher, a true
//	                            return halts iteration over the
//	                            remaining listeners.
//
//	ShouldHandle                ShouldHandle(event) bool gates listener
//	                            execution; returning false skips the
//	                            listener without recording a failure.
//
//	ShouldDispatchAfterCommit   ShouldDispatchAfterCommit() bool defers
//	                            listener execution until the surrounding
//	                            ORM transaction commits. When the gate
//	                            returns true and ctx carries an
//	                            after-commit queue
//	                            (HasAfterCommitQueue == true), the
//	                            dispatcher enqueues the invocation via
//	                            EnqueueAfterCommit instead of firing
//	                            inline; a rollback drops the side
//	                            effect. Outside a transaction the
//	                            listener fires inline as if the
//	                            interface were not implemented.
//
// # Optional event capabilities
//
//	StoppableEvent              ShouldStopPropagation() /
//	                            StopPropagation() let an event signal
//	                            mid-dispatch that no further listeners
//	                            should run. Embed BaseStoppableEvent to
//	                            satisfy the interface without writing
//	                            the bookkeeping fields.
//
// # Lifecycle hooks
//
// Cross-cutting lifecycle hooks (contract.ShutdownAware) are defined in
// the contract package and apply uniformly to every Velocity manager
// that holds background resources; they are not duplicated in each
// package's capability table.
//
// Capability detection is a plain type assertion at the call site
// (dispatcher loops check listener/event for the optional interface
// before invoking the capability-specific path).
package events
