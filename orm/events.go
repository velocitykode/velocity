package orm

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
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

// TransactionExecuted is dispatched at the end of a Manager.Transaction body,
// on commit or rollback. It groups a tx into a single APM node by giving the
// exporter the tx span itself plus a count of statements that ran under it.
// Error is empty on commit and populated on rollback (closure error, panic,
// or commit failure).
type TransactionExecuted struct {
	Context    context.Context
	Connection string        // Database driver name
	Duration   time.Duration // Wall time from BeginTx success to Commit / Rollback resolution
	Statements int           // Number of QueryExecuted events emitted under this tx span
	Error      string        // Empty on commit, populated on rollback / panic / commit failure
	TraceID    string        // APM trace ID
	SpanID     string        // The tx span ID; per-statement events under this tx report it as ParentID
	ParentID   string        // The span that opened this tx (caller's prior span)
}

// Name returns the canonical event name.
func (e *TransactionExecuted) Name() string {
	return "transaction.executed"
}

// txStatementCounterKey scopes a per-tx atomic counter onto the ctx that
// dispatchQueryExecuted increments for each emitted event. The counter lives
// only for the duration of one Manager.Transaction call so a TransactionExecuted
// event can report the exact number of statements that ran under its span.
type txStatementCounterKey struct{}

// withTxStatementCounter installs a fresh counter on ctx. dispatchQueryExecuted
// reads it via txStatementCounter to bump on every emitted QueryExecuted event.
func withTxStatementCounter(ctx context.Context, c *atomic.Int32) context.Context {
	return context.WithValue(ctx, txStatementCounterKey{}, c)
}

// txStatementCounter returns the per-tx counter installed by Manager.Transaction,
// or nil when ctx is not running inside a tx body.
func txStatementCounter(ctx context.Context) *atomic.Int32 {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(txStatementCounterKey{}).(*atomic.Int32)
	return c
}

// txSpanIDKey scopes the surrounding tx span ID onto ctx so a nested
// Manager.Transaction call can parent its own tx span under the outer one.
// Without this, the inner call would read trace.GetSpanID(ctx), which points
// at the outer tx body's stmt-root span (not the outer tx span itself).
type txSpanIDKey struct{}

func withTxSpanID(ctx context.Context, txSpanID string) context.Context {
	return context.WithValue(ctx, txSpanIDKey{}, txSpanID)
}

func txSpanIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(txSpanIDKey{}).(string)
	return s
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
	if c := txStatementCounter(ctx); c != nil {
		c.Add(1)
	}
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
