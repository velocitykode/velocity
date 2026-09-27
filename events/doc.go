// Package events implements the framework's event dispatcher with
// synchronous, asynchronous, and queue-backed listener execution.
//
// # Event names
//
// Every framework event implements contract.Event, and its name follows one
// rule: dot-separated segments of lowercase letters, read as subsystem,
// subject, verb.
//
//   - The subsystem is the package that emits the event: auth, bond, bus,
//     cache, crypto, csrf, events, grpc, httpclient, mail, notification,
//     orm, queue, router or scheduler.
//   - The subject is zero or more nouns naming what the event is about
//     (request, query, transaction, job, batch, task, command, session). It
//     is left out when the subsystem is itself the subject (cache.hit,
//     mail.failed).
//   - The verb is one past-tense verb. A run of work names its stages with
//     started when it begins, completed when it ends and failed when it
//     ends in an error, never with a synonym such as handled, processed,
//     finished, executed or sent. Any other event names what happened:
//     routed, queued, retried, created, cancelled, stopped, recovered, hit,
//     missed, written, forgotten, decrypted or needed.
//
// For example router.request.completed, queue.job.failed,
// scheduler.task.started and cache.missed.
//
// # Listening
//
// Listen takes a key that says which events reach the listener:
//
//	"queue.job.failed"    the event with that name
//	[]string{...}         each of those names
//	an event value        the name that value resolves to
//
// An event's name is what its Name method returns (contract.Event), the
// string itself for a string event, and otherwise its type name split at
// capitals and joined with dots (OrderShipped is order.shipped).
//
// A key that holds a "*" is a pattern. Its one "*" stands for any run of
// characters, dots included, and a name matches when it starts with the
// text before the "*" and ends with the text after it, the two not
// overlapping: "*" matches every name, "queue.*" every name under queue,
// "*.failed" every name that ends in failed, and "queue.*.failed" every
// queue name that ends in failed. A key with more than one "*" matches no
// name. The same matching decides FakeDispatcher's assertions.
//
// # Optional listener capabilities
//
// Listeners MAY implement any of these to opt into framework features:
//
//	QueueableListener     Async() returns true so the dispatcher
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
