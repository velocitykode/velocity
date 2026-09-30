// Package ownctx builds contexts the framework owns from a caller's, for
// work the framework does while it holds a lock.
//
// A caller's context is user code: its Done, Err, Value and Deadline
// methods may panic, block, or call back into the component that called
// them, and database/sql, the context package's With functions and most
// drivers call them on the context they are handed. Under a component's
// lock a call back deadlocks every other caller of the component. So a
// component reads what it needs from the caller's context before it takes
// the lock (Bridge, Detached), and hands the code it runs under the lock
// the context these return, whose methods are framework code only:
//
//   - Done is the channel the caller's Done returned, read once before the
//     lock: waiting on a channel runs no user code;
//   - Deadline is the caller's deadline, read before the lock;
//   - Err is context.Canceled or context.DeadlineExceeded once Done is
//     closed (DeadlineExceeded when the deadline has passed), nil before:
//     the caller's own error value, and its cause, are not carried;
//   - Value answers the framework's correlation ids only (the request id
//     and the trace, span and parent span ids, read before the lock), so a
//     statement event or log line written under the lock carries them;
//     every other key, a caller's own values included, answers nil.
//
// It imports the standard library and internal/tracekeys only.
package ownctx
