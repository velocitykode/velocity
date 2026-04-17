package orm

import (
	"context"
	"runtime"
	"strings"
	"time"
)

// Event is the typed contract every ORM event satisfies. Dispatchers receive
// Event — not interface{} — so handlers can inspect the event name before
// attempting a type assertion.
//
// Naming convention: package.snake_case (e.g. "query.executed", "query.failed").
type Event interface {
	EventName() string
}

// QueryExecuted is dispatched when a database query completes
type QueryExecuted struct {
	Context      context.Context
	SQL          string
	Bindings     []any
	Duration     time.Duration
	RowsAffected int64
	Connection   string // Database connection/driver name
	File         string // Caller file
	Line         int    // Caller line
	TraceID      string // APM trace ID
	SpanID       string // APM span ID
	ParentID     string // Parent span ID for correlation
}

// EventName returns the canonical event name.
func (e *QueryExecuted) EventName() string {
	return "query.executed"
}

// Name is retained for backward compatibility with consumers that predate the
// Event interface. Prefer EventName.
//
// Deprecated: use EventName.
func (e *QueryExecuted) Name() string {
	return e.EventName()
}

// QueryFailed is dispatched when a database query fails
type QueryFailed struct {
	Context    context.Context
	Connection string
	Query      string // parameterized form only, never bound values
	Error      string
	At         time.Time
	TraceID    string // APM trace ID
	SpanID     string // APM span ID
	ParentID   string // Parent span ID for correlation
}

// EventName returns the canonical event name.
func (e *QueryFailed) EventName() string {
	return "query.failed"
}

// Name is retained for backward compatibility with consumers that predate the
// Event interface. Prefer EventName.
//
// Deprecated: use EventName.
func (e *QueryFailed) Name() string {
	return e.EventName()
}

// TxRecover is dispatched when the Manager.Transaction helper recovers from
// a rollback failure. The event always names the recovery cause ("panic" or
// "error") and includes both the rollback error and the originating
// panic/error so observability pipelines can correlate the failure chain.
type TxRecover struct {
	Cause       string // "panic" or "error"
	PanicValue  string // set when Cause == "panic"
	OriginalErr string // set when Cause == "error"
	RollbackErr string // the rollback failure message
}

// EventName returns the canonical event name.
func (e *TxRecover) EventName() string {
	return "orm.tx_recover"
}

// captureCallerInfo captures the file and line of the caller
// skip specifies how many stack frames to skip (0 = captureCallerInfo itself)
func captureCallerInfo(skip int) (file string, line int) {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown", 0
	}

	// Trim the file path to just the last few segments for readability
	// e.g., "/path/to/project/pkg/orm/query.go" -> "pkg/orm/query.go"
	parts := strings.Split(file, "/")
	if len(parts) > 3 {
		file = strings.Join(parts[len(parts)-3:], "/")
	}

	return file, line
}

// dispatchQueryExecuted is a no-op after DI refactor.
// Event dispatching is handled through Manager.dispatchEvent().
func dispatchQueryExecuted(_ context.Context, _ string, _ []any, _ time.Duration, _ int64, _ string, _ int) {
}
