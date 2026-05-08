package orm

import (
	"context"
	"runtime"
	"strings"
	"time"

	"github.com/velocitykode/velocity/trace"
)

// Event is the typed contract every ORM event satisfies. Matches the shape of
// events.Event and scheduler.Event so dispatchers can accept events from any
// package through a single interface.
//
// Naming convention: package.snake_case (e.g. "query.executed", "query.failed").
type Event interface {
	Name() string
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

// Name returns the canonical event name.
func (e *QueryExecuted) Name() string {
	return "query.executed"
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

// Name returns the canonical event name.
func (e *QueryFailed) Name() string {
	return "query.failed"
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

// Name returns the canonical event name.
func (e *TxRecover) Name() string {
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

// dispatchQueryExecuted constructs a QueryExecuted event and routes it
// through the default Manager's dispatcher. No-ops when no default Manager
// is set or no event dispatcher is wired. skip is the caller-frame skip
// count for captureCallerInfo (typically 2: caller of dispatch + dispatch
// itself).
func dispatchQueryExecuted(ctx context.Context, sql string, args []any, dur time.Duration, rows int64, conn string, skip int) {
	m := Default()
	if m == nil {
		return
	}
	m.mu.RLock()
	hasDispatcher := m.eventDispatcher != nil
	m.mu.RUnlock()
	if !hasDispatcher {
		return
	}
	file, line := captureCallerInfo(skip + 1)
	m.dispatchEvent(ctx, &QueryExecuted{
		Context:      ctx,
		SQL:          sql,
		Bindings:     args,
		Duration:     dur,
		RowsAffected: rows,
		Connection:   conn,
		File:         file,
		Line:         line,
		TraceID:      trace.GetTraceID(ctx),
		SpanID:       trace.GetSpanID(ctx),
		ParentID:     trace.GetParentID(ctx),
	})
}
