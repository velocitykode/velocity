package orm

import (
	"context"
	"runtime"
	"strings"
	"time"
)

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

// Name returns the event name
func (e *QueryExecuted) Name() string {
	return "query.executed"
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
