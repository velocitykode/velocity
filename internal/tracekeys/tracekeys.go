// Package tracekeys holds the context keys under which the trace package
// stores the request id and the trace, span and parent span ids. They
// live here, not in trace, so internal/ownctx can answer them from a
// context the framework owns without trace importing it or exporting the
// keys. Only trace and ownctx use them.
//
// It imports nothing.
package tracekeys

// Key is the type of the keys below: unexported by the trace package's
// own API, so no other package can collide with them.
type Key string

// The keys, with the values the trace package has always used.
const (
	RequestID Key = "velocity_request_id"
	TraceID   Key = "velocity_trace_id"
	SpanID    Key = "velocity_span_id"
	ParentID  Key = "velocity_parent_id"
)
