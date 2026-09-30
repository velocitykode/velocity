package orm

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/trace"
)

// QueryExecuted is dispatched when a database query completes. It fires for
// every statement that reaches the database through an instrumented
// connection - the query builder, Manager.Raw / Manager.Exec, statements
// inside a transaction, prepared statements, and subsystems that hold the raw
// *sql.DB - because the event is emitted from the database/sql driver wrapper
// rather than from individual call sites.
//
// Delivery is asynchronous. The statement is recorded inside a driver
// callback, where running a listener would hold a database connection hostage,
// so listeners run afterwards on a delivery goroutine in the order the
// statements completed. Two consequences: a listener has not necessarily seen
// a query by the time the call issuing it returns (use
// Manager.FlushQueryEvents at a boundary that needs it), and a
// TransactionExecuted may arrive before the statements it summarises - its
// Statements count is still exact, because the counter advances when the
// statement is recorded rather than when it is delivered.
type QueryExecuted struct {
	contract.EventMeta
	SQL string
	// Bindings are the bound parameters as the database driver received
	// them, after database/sql's conversion: an int arrives as int64, and a
	// driver.Valuer has already been resolved to its underlying value.
	// They are a snapshot taken when the statement ran: a []byte is a
	// copy, so a caller reusing its buffer after the statement returned
	// does not change the event. The copy is made only when a dispatcher
	// is set, for a statement that binds a []byte. A named byte-slice
	// type or another driver-specific value a driver lets through is
	// delivered by reference, as the caller passed it.
	Bindings []any
	// Duration is the wall time the statement took. For a statement that
	// returns rows it spans from issue until the result set is closed, so
	// it covers row transfer and scanning, not just the round trip.
	Duration time.Duration
	// RowsAffected is the driver-reported affected-row count for a write,
	// or the number of rows the caller consumed from a read.
	RowsAffected int64
	// Slow reports that Duration exceeded the connection's slow threshold
	// (ManagerConfig.SlowThreshold, DB_SLOW_QUERY_THRESHOLD in an app).
	// The same statement wrote the "velocity/orm: slow query" warn line.
	// Always false when the threshold is zero.
	Slow       bool
	Connection string // Database connection/driver name
	File       string // Caller file
	Line       int    // Caller line
}

// Name returns the canonical event name.
func (e *QueryExecuted) Name() string {
	return "orm.query.completed"
}

// QueryFailed is dispatched when a database query fails, on the same
// driver-level path as QueryExecuted: exactly one of the two fires per
// statement. Statements that fail mid-stream (an error from a later row read
// rather than from issuing the query) report here too.
//
// The sql package's internal control-flow sentinels are not failures and never
// produce this event: driver.ErrSkip means one execution path declined in
// favour of another that reports its own outcome, and driver.ErrBadConn means
// the statement is being retried on a fresh connection.
//
// Both events' SpanID is the statement's own span and ParentID the span it
// ran under (the transaction's span inside a transaction).
type QueryFailed struct {
	contract.EventMeta
	Connection string
	Query      string // parameterized form only, never bound values
	// Err is the error the driver returned.
	Err error
	// Duration is the wall time spent before the failure surfaced.
	Duration time.Duration
	File     string // Caller file
	Line     int    // Caller line
}

// Name returns the canonical event name.
func (e *QueryFailed) Name() string {
	return "orm.query.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e QueryFailed) MarshalJSON() ([]byte, error) {
	type fields QueryFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *QueryFailed) UnmarshalJSON(data []byte) error {
	type fields QueryFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// TxRecover is dispatched when the Manager.Transaction helper recovers from
// a rollback failure, and when a transaction callback panics. The event
// always names the recovery cause ("panic", "error" or "callback_panic")
// and includes both the rollback error and the originating panic/error so
// observability pipelines can correlate the failure chain.
type TxRecover struct {
	contract.EventMeta
	Cause       string // "panic", "error" or "callback_panic"
	PanicValue  string // set when Cause is "panic" or "callback_panic"
	OriginalErr error  // set when Cause == "error"
	RollbackErr error  // the rollback failure; nil for "callback_panic"
}

// Name returns the canonical event name.
func (e *TxRecover) Name() string {
	return "orm.transaction.recovered"
}

// MarshalJSON encodes the event with OriginalErr and RollbackErr as their
// text.
func (e TxRecover) MarshalJSON() ([]byte, error) {
	type fields TxRecover
	return json.Marshal(struct {
		fields
		OriginalErr string `json:",omitempty"`
		RollbackErr string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.OriginalErr), eventmeta.ErrorText(e.RollbackErr)})
}

// UnmarshalJSON decodes the event's JSON form: OriginalErr and RollbackErr
// become errors with the encoded text.
func (e *TxRecover) UnmarshalJSON(data []byte) error {
	type fields TxRecover
	v := struct {
		*fields
		OriginalErr string `json:",omitempty"`
		RollbackErr string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.OriginalErr = eventmeta.TextError(v.OriginalErr)
	e.RollbackErr = eventmeta.TextError(v.RollbackErr)
	return nil
}

// TransactionExecuted is dispatched at the end of a Manager.Transaction body,
// on commit or rollback. It groups a tx into a single APM node by giving the
// exporter the tx span itself plus a count of statements that ran under it.
// Err is nil on commit and set on rollback (the closure's error, the panic,
// or the commit failure). SpanID is the transaction's span, which the
// statements under it report as their ParentID, and ParentID the span that
// opened it.
type TransactionExecuted struct {
	contract.EventMeta
	Connection string        // Database driver name
	Duration   time.Duration // Wall time from BeginTx success to Commit / Rollback resolution
	Statements int           // Number of QueryExecuted events emitted under this tx span
	Err        error         // nil on commit, set on rollback / panic / commit failure
}

// Name returns the canonical event name.
func (e *TransactionExecuted) Name() string {
	return "orm.transaction.completed"
}

// MarshalJSON encodes the event with Err as its text.
func (e TransactionExecuted) MarshalJSON() ([]byte, error) {
	type fields TransactionExecuted
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *TransactionExecuted) UnmarshalJSON(data []byte) error {
	type fields TransactionExecuted
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// txStatementCounterKey scopes a per-tx atomic counter onto the ctx that the
// statement observer increments for each recorded event. The counter lives
// only for the duration of one Manager.Transaction call so a TransactionExecuted
// event can report the exact number of statements that ran under its span.
type txStatementCounterKey struct{}

// withTxStatementCounter installs a fresh counter on ctx. The statement
// observer reads it via txStatementCounter to bump on every recorded
// QueryExecuted event.
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

// captureCallerInfo captures the file and line of the caller
// skip specifies how many stack frames to skip (0 = captureCallerInfo itself)
func captureCallerInfo(skip int) (file string, line int) {
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return "unknown", 0
	}
	return trimCallerFile(file), line
}

// trimCallerFile shortens an absolute source path to its last few segments for
// readability, e.g. "/path/to/project/pkg/orm/query.go" -> "pkg/orm/query.go".
func trimCallerFile(file string) string {
	parts := strings.Split(file, "/")
	if len(parts) > 3 {
		return strings.Join(parts[len(parts)-3:], "/")
	}
	return file
}

// frameworkPkgPrefix is the import-path prefix of every package in this
// module. Frames below it sit between the application and the database and
// are never the answer to "where did this query come from".
const frameworkPkgPrefix = "github.com/velocitykode/velocity/"

// applicationCaller walks the stack outward from the driver-level observation
// point and returns the first frame that belongs to application code.
//
// Statements are now observed inside the database/sql driver wrapper, so the
// frames between the caller and the observer vary by execution path (direct
// Queryer, prepared statement, deferred rows.Close) and a fixed skip count
// cannot address the caller. Instead every frame belonging to the sql
// machinery or to this module is skipped. Test sources are treated as
// application code even though they live in this module, so in-repo tests
// still see their own file.
//
// Returns "unknown", 0 when no application frame exists - the case when
// database/sql closes a result set from its context-cancellation goroutine.
func applicationCaller() (string, int) {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:])
	if n == 0 {
		return "unknown", 0
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.File != "" && !isInfrastructureFrame(f.Function, f.File) {
			return trimCallerFile(f.File), f.Line
		}
		if !more {
			return "unknown", 0
		}
	}
}

// isInfrastructureFrame reports whether a stack frame belongs to the plumbing
// between the application and the database rather than to the application.
func isInfrastructureFrame(fn, file string) bool {
	// A test source is application code wherever it lives, including inside
	// this module.
	if strings.HasSuffix(file, "_test.go") {
		return false
	}
	if strings.HasPrefix(fn, frameworkPkgPrefix) {
		return true
	}
	// Stdlib plumbing that can sit between the application and the driver.
	// Kept to an explicit list rather than "any import path without a dot"
	// so a query issued straight from an application's main package still
	// resolves to its own source.
	for _, pkg := range [...]string{
		"database/sql.",
		"database/sql/driver.",
		"runtime.",
		"sync.",
		"sync/atomic.",
		"reflect.",
	} {
		if strings.HasPrefix(fn, pkg) {
			return true
		}
	}
	return false
}

// managerObserver turns driver-level statement executions into ORM events for
// one Manager. Attaching it at the driver layer is what makes query telemetry
// unconditional: the query builder, Manager.Raw / Manager.Exec, statements
// issued inside a *sql.Tx, and subsystems holding a raw *sql.DB (the auth user
// store) all reach the database through the same instrumented connection,
// so none of them has to opt in.
//
// It is bound to the manager that owns the pool, not to a process-wide
// default, so each manager's statements dispatch through its own dispatcher.
type managerObserver struct {
	m *Manager
}

var _ drivers.StatementObserver = managerObserver{}

// Observing reports whether a dispatcher is wired to receive the events. The
// instrumentation calls this before timing a statement, so a manager with no
// listener pays one atomic load per statement.
func (o managerObserver) Observing() bool {
	return o.m.hasDispatcher.Load()
}

// ObserveStatement builds QueryExecuted for a completed statement, or
// QueryFailed for a failed one, and hands it to the delivery pump.
//
// This runs inside a database/sql driver callback: the connection's lock is
// held and the connection has not yet returned to the pool, so nothing here
// may block or touch the database. Everything that is only available here -
// the caller's stack frames above all - is captured now; the listener runs
// later, on the pump goroutine.
func (o managerObserver) ObserveStatement(ev drivers.StatementEvent) {
	p := o.m.pump.Load()
	if p == nil {
		return
	}
	ctx := ev.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// Only valid on this goroutine, at this instant.
	file, line := applicationCaller()
	// Each statement is its own span under the caller's span (a root span
	// when ctx carries no trace).
	traceID, spanID, parentID := trace.ChildSpanIDs(ctx)

	meta := contract.EventMeta{Context: ctx, TraceID: traceID, SpanID: spanID, ParentID: parentID, At: time.Now()}
	if ev.Err != nil {
		p.enqueue(ctx, &QueryFailed{
			EventMeta:  meta,
			Connection: ev.Connection,
			Query:      ev.SQL,
			Err:        ev.Err,
			Duration:   ev.Duration,
			File:       file,
			Line:       line,
		})
		return
	}

	// The counter advances here rather than at delivery so
	// TransactionExecuted.Statements is exact even though the statement
	// events it summarises are still queued when it fires.
	if c := txStatementCounter(ctx); c != nil {
		c.Add(1)
	}
	p.enqueue(ctx, &QueryExecuted{
		EventMeta:    meta,
		SQL:          ev.SQL,
		Bindings:     ev.Args,
		Duration:     ev.Duration,
		RowsAffected: ev.RowsAffected,
		Slow:         ev.Slow,
		Connection:   ev.Connection,
		File:         file,
		Line:         line,
	})
}
