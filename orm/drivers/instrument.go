package drivers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync/atomic"
	"time"
)

// StatementEvent describes one SQL statement executed through an instrumented
// connection. It is the single, driver-level observation point for query
// telemetry: every statement issued against a database opened with
// OpenInstrumented produces exactly one event, whether it originated in the
// ORM query builder, Manager.Raw / Manager.Exec, a raw *sql.DB held by another
// subsystem (the auth user provider), a *sql.Tx, or a prepared *sql.Stmt.
//
// Err distinguishes the two outcomes: nil for a completed statement, non-nil
// for a failure. Control-flow sentinels the sql package uses internally
// (driver.ErrSkip, driver.ErrBadConn) never surface as events.
type StatementEvent struct {
	// Context is the context the statement executed under, carrying trace
	// and transaction-scoped values from the caller.
	Context context.Context
	// Connection is the logical connection name supplied when the pool was
	// opened (e.g. "sqlite", "postgres", "mysql").
	Connection string
	// SQL is the statement text as handed to the driver.
	SQL string
	// Args are the bound parameters after database/sql has converted them
	// to driver.Value. They are therefore the driver's view of the
	// arguments, not the caller's: an int arrives as int64, and a
	// driver.Valuer has already been resolved to its underlying value.
	// Nil on the failure path, where bound values must not be recorded.
	Args []any
	// Duration is the wall time the statement took. For a statement
	// returning rows it spans from issue to the point the result set is
	// closed, so it includes row transfer and scan time.
	Duration time.Duration
	// RowsAffected is the row count reported by the driver for a write, or
	// the number of rows produced for a read. Zero on failure and when the
	// driver reports no count.
	RowsAffected int64
	// Err is nil when the statement completed, otherwise the failure.
	Err error
}

// StatementObserver receives one callback per statement executed through an
// instrumented connection. It is the seam the orm package uses to turn
// driver-level execution into QueryExecuted / QueryFailed events without the
// drivers package importing orm.
//
// CRITICAL: ObserveStatement runs inside a database/sql driver callback, which
// means it holds that connection's lock and runs before the connection is
// returned to the pool. An implementation MUST NOT block, and MUST NOT issue
// another database call on the same pool - with a small MaxOpenConns the
// second call waits for a connection that cannot be released until the
// callback returns. Implementations are expected to capture what they need
// (including any stack-derived data, which is only valid here) and hand the
// event off for delivery elsewhere.
type StatementObserver interface {
	// Observing reports whether the observer currently wants events. The
	// instrumentation consults it before timing a statement or wrapping its
	// result set, so a pool with no observer attached pays one atomic load
	// plus one interface call per statement and allocates nothing.
	Observing() bool
	// ObserveStatement is called exactly once per executed statement.
	ObserveStatement(ev StatementEvent)
}

// StatementObservable is implemented by drivers whose pool can report executed
// statements. BaseDriver implements it, so every driver embedding BaseDriver
// and opening its pool through OpenAndPing or BaseDriver.OpenInstrumented
// satisfies it for free.
//
// The ORM manager type-asserts its driver to this interface and attaches
// itself, which is what routes a pool's telemetry to the manager that owns it
// rather than to a process-wide default.
type StatementObservable interface {
	// SetStatementObserver attaches o to the pool this driver opened.
	// Passing nil detaches the current observer. Attaching replaces any
	// previous observer: a driver handed to two managers reports to
	// whichever attached last.
	SetStatementObserver(o StatementObserver)
}

// observerBinding is the mutable link between an opened pool and its observer.
// The pool is created while the driver connects, which happens before the
// owning manager exists, so the binding starts empty and is filled in once the
// manager attaches itself.
type observerBinding struct {
	name string
	obs  atomic.Pointer[StatementObserver]
}

func (b *observerBinding) set(o StatementObserver) {
	if o == nil {
		b.obs.Store(nil)
		return
	}
	b.obs.Store(&o)
}

// active returns the attached observer when it currently wants events,
// otherwise nil. This is the fast-path guard: no timing, no result-set
// wrapping, and no argument copying happen when it returns nil.
func (b *observerBinding) active() StatementObserver {
	p := b.obs.Load()
	if p == nil {
		return nil
	}
	o := *p
	if o == nil || !o.Observing() {
		return nil
	}
	return o
}

// isControlErr reports whether err is one of the sql package's internal
// control-flow sentinels rather than a statement failure. driver.ErrSkip means
// "this execution path declined, fall back to another one" (the prepared-
// statement path then runs and reports its own event), and driver.ErrBadConn
// means "retry on a fresh connection". Reporting either as query.failed would
// fabricate failures for statements that go on to succeed.
func isControlErr(err error) bool {
	return errors.Is(err, driver.ErrSkip) || errors.Is(err, driver.ErrBadConn)
}

// openInstrumented opens a database handle whose every statement is reported
// to the observer attached to the returned binding.
//
// The handle is built with sql.OpenDB over a wrapping connector rather than
// sql.Open, so instrumentation covers every route into the database: *sql.DB,
// *sql.Conn, *sql.Tx, and *sql.Stmt alike.
func openInstrumented(sqlDriverName, connectionName, dsn string) (*sql.DB, *observerBinding, error) {
	// database/sql exposes no way to look up a registered driver value, so
	// open a throwaway handle to read it back. sql.Open does not dial, and
	// closing the probe releases only the connector it built for itself.
	probe, err := sql.Open(sqlDriverName, dsn)
	if err != nil {
		return nil, nil, err
	}
	drv := probe.Driver()
	if err := probe.Close(); err != nil {
		return nil, nil, err
	}

	var base driver.Connector
	if dc, ok := drv.(driver.DriverContext); ok {
		base, err = dc.OpenConnector(dsn)
		if err != nil {
			return nil, nil, err
		}
	} else {
		base = dsnConnector{dsn: dsn, drv: drv}
	}

	binding := &observerBinding{name: connectionName}
	return sql.OpenDB(&instrumentedConnector{base: base, binding: binding}), binding, nil
}

// dsnConnector adapts a legacy driver.Driver (one that does not implement
// driver.DriverContext) to the connector interface, mirroring the unexported
// adapter database/sql uses for the same purpose.
type dsnConnector struct {
	dsn string
	drv driver.Driver
}

func (c dsnConnector) Connect(_ context.Context) (driver.Conn, error) {
	return c.drv.Open(c.dsn)
}

func (c dsnConnector) Driver() driver.Driver { return c.drv }

// instrumentedConnector wraps the real connector so every connection handed to
// database/sql is an instrumentedConn.
type instrumentedConnector struct {
	base    driver.Connector
	binding *observerBinding
}

func (c *instrumentedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	inner, err := c.base.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &instrumentedConn{inner: inner, binding: c.binding}, nil
}

func (c *instrumentedConnector) Driver() driver.Driver { return c.base.Driver() }

// Close releases the wrapped connector when it owns resources, matching the
// io.Closer contract database/sql honours on DB.Close.
func (c *instrumentedConnector) Close() error {
	if closer, ok := c.base.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// instrumentedConn wraps a driver connection. It implements every optional
// interface database/sql probes for, forwarding to the wrapped connection when
// it supports the capability and reproducing the sql package's own fallback
// behaviour when it does not. Implementing them unconditionally is required:
// interface satisfaction is static, so a wrapper that omitted one would hide
// the capability from database/sql for every driver.
//
// driver.ColumnConverter is the one exception, because its mere presence
// changes how database/sql converts arguments; see newInstrumentedStmt.
type instrumentedConn struct {
	inner   driver.Conn
	binding *observerBinding
}

var (
	_ driver.Conn               = (*instrumentedConn)(nil)
	_ driver.ConnPrepareContext = (*instrumentedConn)(nil)
	_ driver.ConnBeginTx        = (*instrumentedConn)(nil)
	_ driver.ExecerContext      = (*instrumentedConn)(nil)
	_ driver.QueryerContext     = (*instrumentedConn)(nil)
	_ driver.NamedValueChecker  = (*instrumentedConn)(nil)
	_ driver.SessionResetter    = (*instrumentedConn)(nil)
	_ driver.Validator          = (*instrumentedConn)(nil)
	_ driver.Pinger             = (*instrumentedConn)(nil)
)

func (c *instrumentedConn) Prepare(query string) (driver.Stmt, error) {
	inner, err := c.inner.Prepare(query)
	if err != nil {
		return nil, err
	}
	return newInstrumentedStmt(inner, c, query), nil
}

func (c *instrumentedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var (
		inner driver.Stmt
		err   error
	)
	if pc, ok := c.inner.(driver.ConnPrepareContext); ok {
		inner, err = pc.PrepareContext(ctx, query)
	} else {
		inner, err = c.inner.Prepare(query)
		if err == nil {
			select {
			default:
			case <-ctx.Done():
				_ = inner.Close()
				return nil, ctx.Err()
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return newInstrumentedStmt(inner, c, query), nil
}

func (c *instrumentedConn) Close() error { return c.inner.Close() }

func (c *instrumentedConn) Begin() (driver.Tx, error) { //nolint:staticcheck // required by driver.Conn
	return c.inner.Begin() //nolint:staticcheck // fallback for drivers without ConnBeginTx
}

// BeginTx forwards to the wrapped connection's ConnBeginTx when available and
// otherwise reproduces the sql package's fallback: reject options the legacy
// Begin cannot express, then honour context cancellation around it.
func (c *instrumentedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if bt, ok := c.inner.(driver.ConnBeginTx); ok {
		return bt.BeginTx(ctx, opts)
	}
	if opts.Isolation != driver.IsolationLevel(sql.LevelDefault) {
		return nil, errors.New("sql: driver does not support non-default isolation level")
	}
	if opts.ReadOnly {
		return nil, errors.New("sql: driver does not support read-only transactions")
	}
	if ctx.Done() == nil {
		return c.inner.Begin() //nolint:staticcheck // fallback for drivers without ConnBeginTx
	}
	tx, err := c.inner.Begin() //nolint:staticcheck // fallback for drivers without ConnBeginTx
	if err == nil {
		select {
		default:
		case <-ctx.Done():
			_ = tx.Rollback()
			return nil, ctx.Err()
		}
	}
	return tx, err
}

// CheckNamedValue reproduces the precedence database/sql applies when no
// wrapper is present. On the non-prepared path the sql package consults only
// the connection's checker, so forwarding it (or declining with ErrSkip, which
// routes to the default converter) is exact.
func (c *instrumentedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if nvc, ok := c.inner.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

func (c *instrumentedConn) ResetSession(ctx context.Context) error {
	if sr, ok := c.inner.(driver.SessionResetter); ok {
		return sr.ResetSession(ctx)
	}
	return nil
}

func (c *instrumentedConn) IsValid() bool {
	if v, ok := c.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *instrumentedConn) Ping(ctx context.Context) error {
	if p, ok := c.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

// QueryContext runs a read. On success the returned rows are wrapped so the
// event fires when the result set closes, carrying the number of rows the
// caller actually consumed.
func (c *instrumentedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	obs := c.binding.active()
	if obs == nil {
		return c.queryInner(ctx, query, args)
	}
	start := time.Now()
	rows, err := c.queryInner(ctx, query, args)
	if err != nil {
		reportFailure(obs, ctx, c.binding.name, query, start, err)
		return nil, err
	}
	return newInstrumentedRows(rows, obs, ctx, c.binding.name, query, args, start), nil
}

func (c *instrumentedConn) queryInner(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := c.inner.(driver.QueryerContext); ok {
		return qc.QueryContext(ctx, query, args)
	}
	q, ok := c.inner.(driver.Queryer) //nolint:staticcheck // legacy driver support
	if !ok {
		// Declining here sends database/sql down the prepared-statement
		// path, which reports its own event.
		return nil, driver.ErrSkip
	}
	vals, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return q.Query(query, vals) //nolint:staticcheck // legacy driver support
}

// ExecContext runs a write and reports the event as soon as it completes; the
// affected-row count comes straight from the driver result.
func (c *instrumentedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	obs := c.binding.active()
	if obs == nil {
		return c.execInner(ctx, query, args)
	}
	start := time.Now()
	res, err := c.execInner(ctx, query, args)
	if err != nil {
		reportFailure(obs, ctx, c.binding.name, query, start, err)
		return nil, err
	}
	reportSuccess(obs, ctx, c.binding.name, query, args, start, resultRows(res))
	return res, nil
}

func (c *instrumentedConn) execInner(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := c.inner.(driver.ExecerContext); ok {
		return ec.ExecContext(ctx, query, args)
	}
	e, ok := c.inner.(driver.Execer) //nolint:staticcheck // legacy driver support
	if !ok {
		return nil, driver.ErrSkip
	}
	vals, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.Exec(query, vals) //nolint:staticcheck // legacy driver support
}

// newInstrumentedStmt wraps a prepared statement, exposing
// driver.ColumnConverter if and only if the wrapped statement does.
//
// This conditional matters for correctness rather than tidiness.
// database/sql picks one of four argument-conversion routes from the
// interfaces the statement and connection satisfy (see driverArgsConnLocked):
// with a column converter present, driver.ErrSkip from the named-value checker
// retries against the column converter; without one it falls straight to the
// default converter. A wrapper that always advertised ColumnConverter would
// silently move every driver onto the first route, and one that never
// advertised it would strip the retry from drivers that rely on it. Selecting
// the variant at prepare time keeps the route identical to the unwrapped
// driver's, and lets the sql package's own ccChecker perform the driver.Valuer
// resolution, argument-count guard, and driver.IsValue validation that a
// hand-rolled converter call would omit.
func newInstrumentedStmt(inner driver.Stmt, c *instrumentedConn, query string) driver.Stmt {
	s := instrumentedStmt{inner: inner, conn: c, query: query}
	if _, ok := inner.(driver.ColumnConverter); ok { //nolint:staticcheck // deprecated but still honoured by database/sql
		return &instrumentedStmtColumnConverter{instrumentedStmt: s}
	}
	return &s
}

// instrumentedStmt wraps a prepared statement. Statements reach this path
// either because the caller prepared explicitly or because the connection
// declined the direct Queryer / Execer route; either way the execution is
// reported here and nowhere else, so a statement is never counted twice.
type instrumentedStmt struct {
	inner driver.Stmt
	conn  *instrumentedConn
	query string
}

// instrumentedStmtColumnConverter is the variant used when the wrapped
// statement implements the deprecated driver.ColumnConverter, forwarding it so
// database/sql keeps taking the same conversion route it would without the
// wrapper.
type instrumentedStmtColumnConverter struct {
	instrumentedStmt
}

func (s *instrumentedStmtColumnConverter) ColumnConverter(idx int) driver.ValueConverter {
	return s.inner.(driver.ColumnConverter).ColumnConverter(idx) //nolint:staticcheck // deprecated but still honoured by database/sql
}

var (
	_ driver.Stmt              = (*instrumentedStmt)(nil)
	_ driver.StmtExecContext   = (*instrumentedStmt)(nil)
	_ driver.StmtQueryContext  = (*instrumentedStmt)(nil)
	_ driver.NamedValueChecker = (*instrumentedStmt)(nil)
	_ driver.ColumnConverter   = (*instrumentedStmtColumnConverter)(nil) //nolint:staticcheck // deprecated but still honoured by database/sql
)

func (s *instrumentedStmt) Close() error  { return s.inner.Close() }
func (s *instrumentedStmt) NumInput() int { return s.inner.NumInput() }

func (s *instrumentedStmt) Exec(args []driver.Value) (driver.Result, error) { //nolint:staticcheck // required by driver.Stmt
	return s.ExecContext(context.Background(), valuesToNamedValues(args))
}

func (s *instrumentedStmt) Query(args []driver.Value) (driver.Rows, error) { //nolint:staticcheck // required by driver.Stmt
	return s.QueryContext(context.Background(), valuesToNamedValues(args))
}

func (s *instrumentedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	obs := s.conn.binding.active()
	if obs == nil {
		return s.execInner(ctx, args)
	}
	start := time.Now()
	res, err := s.execInner(ctx, args)
	if err != nil {
		reportFailure(obs, ctx, s.conn.binding.name, s.query, start, err)
		return nil, err
	}
	reportSuccess(obs, ctx, s.conn.binding.name, s.query, args, start, resultRows(res))
	return res, nil
}

func (s *instrumentedStmt) execInner(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if ec, ok := s.inner.(driver.StmtExecContext); ok {
		return ec.ExecContext(ctx, args)
	}
	vals, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.Exec(vals) //nolint:staticcheck // fallback for drivers without StmtExecContext
}

func (s *instrumentedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	obs := s.conn.binding.active()
	if obs == nil {
		return s.queryInner(ctx, args)
	}
	start := time.Now()
	rows, err := s.queryInner(ctx, args)
	if err != nil {
		reportFailure(obs, ctx, s.conn.binding.name, s.query, start, err)
		return nil, err
	}
	return newInstrumentedRows(rows, obs, ctx, s.conn.binding.name, s.query, args, start), nil
}

func (s *instrumentedStmt) queryInner(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if qc, ok := s.inner.(driver.StmtQueryContext); ok {
		return qc.QueryContext(ctx, args)
	}
	vals, err := namedValuesToValues(args)
	if err != nil {
		return nil, err
	}
	select {
	default:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.inner.Query(vals) //nolint:staticcheck // fallback for drivers without StmtQueryContext
}

// CheckNamedValue reproduces database/sql's checker selection for a prepared
// statement: the statement's own checker wins, otherwise the connection's, and
// driver.ErrSkip declines to whatever the sql package would try next (the
// column converter when one is exposed, else the default converter). Errors
// from the wrapped checker - including driver.ErrRemoveArgument - are returned
// verbatim so the sql package handles them as it normally would.
//
// The wrapper has to run the statement-then-connection selection itself
// because implementing NamedValueChecker at all makes database/sql stop
// looking at the connection.
func (s *instrumentedStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if nvc, ok := s.inner.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	if nvc, ok := s.conn.inner.(driver.NamedValueChecker); ok {
		return nvc.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// instrumentedRows wraps a result set so the statement's event can carry the
// number of rows the caller consumed. The event fires when the result set is
// closed, which database/sql guarantees: it closes rows on exhaustion, on
// Row.Scan, and on context cancellation.
type instrumentedRows struct {
	inner driver.Rows
	obs   StatementObserver
	ev    StatementEvent
	start time.Time

	// count is written by Next and read by finish, which database/sql may
	// call from its context-cancellation goroutine. The sql package
	// serialises the two behind its own mutex; the atomic makes the
	// ordering explicit rather than inherited.
	count atomic.Int64
	// streamErr records the first non-EOF error Next returned, so a result
	// set that fails mid-stream is reported as a failure rather than a
	// short success.
	streamErr atomic.Pointer[error]
	// emitted guards the event: Close is not required to be called only
	// once, and database/sql closes rows both on exhaustion and
	// explicitly. A compare-and-swap rather than a sync.Once because the
	// observer walks the stack to attribute the statement to its caller,
	// and Once.Do would insert a stdlib frame between the two.
	emitted atomic.Bool
}

var (
	_ driver.Rows                           = (*instrumentedRows)(nil)
	_ driver.RowsNextResultSet              = (*instrumentedRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*instrumentedRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*instrumentedRows)(nil)
	_ driver.RowsColumnTypeLength           = (*instrumentedRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*instrumentedRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*instrumentedRows)(nil)
)

func newInstrumentedRows(inner driver.Rows, obs StatementObserver, ctx context.Context, conn, query string, args []driver.NamedValue, start time.Time) *instrumentedRows {
	return &instrumentedRows{
		inner: inner,
		obs:   obs,
		ev: StatementEvent{
			Context:    ctx,
			Connection: conn,
			SQL:        query,
			Args:       namedValuesToAny(args),
		},
		start: start,
	}
}

func (r *instrumentedRows) Columns() []string { return r.inner.Columns() }

func (r *instrumentedRows) Next(dest []driver.Value) error {
	err := r.inner.Next(dest)
	switch {
	case err == nil:
		r.count.Add(1)
	case errors.Is(err, io.EOF):
		// Normal exhaustion; the close that follows reports the event.
	default:
		if r.streamErr.Load() == nil {
			r.streamErr.Store(&err)
		}
	}
	return err
}

func (r *instrumentedRows) Close() error {
	err := r.inner.Close()
	r.finish(err)
	return err
}

// finish emits the single event for this result set. The close error is
// preferred over a mid-stream Next error only when no stream error was seen,
// since the stream error is the one that explains a truncated read.
func (r *instrumentedRows) finish(closeErr error) {
	if !r.emitted.CompareAndSwap(false, true) {
		return
	}
	ev := r.ev
	ev.Duration = time.Since(r.start)
	if se := r.streamErr.Load(); se != nil {
		ev.Err = *se
	} else if closeErr != nil {
		ev.Err = closeErr
	}
	if ev.Err != nil {
		if isControlErr(ev.Err) {
			return
		}
		// A failed read must not carry bound values.
		ev.Args = nil
	} else {
		ev.RowsAffected = r.count.Load()
	}
	r.obs.ObserveStatement(ev)
}

func (r *instrumentedRows) HasNextResultSet() bool {
	if nrs, ok := r.inner.(driver.RowsNextResultSet); ok {
		return nrs.HasNextResultSet()
	}
	return false
}

func (r *instrumentedRows) NextResultSet() error {
	if nrs, ok := r.inner.(driver.RowsNextResultSet); ok {
		return nrs.NextResultSet()
	}
	return io.EOF
}

func (r *instrumentedRows) ColumnTypeScanType(index int) reflect.Type {
	if ct, ok := r.inner.(driver.RowsColumnTypeScanType); ok {
		return ct.ColumnTypeScanType(index)
	}
	// The fallback database/sql uses when a driver does not report a scan
	// type.
	return reflect.TypeOf(new(any)).Elem()
}

func (r *instrumentedRows) ColumnTypeDatabaseTypeName(index int) string {
	if ct, ok := r.inner.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return ct.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}

func (r *instrumentedRows) ColumnTypeLength(index int) (int64, bool) {
	if ct, ok := r.inner.(driver.RowsColumnTypeLength); ok {
		return ct.ColumnTypeLength(index)
	}
	return 0, false
}

func (r *instrumentedRows) ColumnTypeNullable(index int) (nullable, ok bool) {
	if ct, is := r.inner.(driver.RowsColumnTypeNullable); is {
		return ct.ColumnTypeNullable(index)
	}
	return false, false
}

func (r *instrumentedRows) ColumnTypePrecisionScale(index int) (precision, scale int64, ok bool) {
	if ct, is := r.inner.(driver.RowsColumnTypePrecisionScale); is {
		return ct.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}

// reportSuccess emits a completed-statement event.
func reportSuccess(obs StatementObserver, ctx context.Context, conn, query string, args []driver.NamedValue, start time.Time, rows int64) {
	obs.ObserveStatement(StatementEvent{
		Context:      ctx,
		Connection:   conn,
		SQL:          query,
		Args:         namedValuesToAny(args),
		Duration:     time.Since(start),
		RowsAffected: rows,
	})
}

// reportFailure emits a failed-statement event, dropping control-flow
// sentinels and never recording bound values.
func reportFailure(obs StatementObserver, ctx context.Context, conn, query string, start time.Time, err error) {
	if isControlErr(err) {
		return
	}
	obs.ObserveStatement(StatementEvent{
		Context:    ctx,
		Connection: conn,
		SQL:        query,
		Duration:   time.Since(start),
		Err:        err,
	})
}

// resultRows reads the affected-row count from a driver result, reporting zero
// when the driver does not track one (driver.ResultNoRows and friends).
func resultRows(res driver.Result) int64 {
	if res == nil {
		return 0
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0
	}
	return n
}

// namedValuesToValues strips the names off driver arguments for the legacy
// (non-context) driver entry points, rejecting named parameters exactly as
// database/sql does.
func namedValuesToValues(named []driver.NamedValue) ([]driver.Value, error) {
	vals := make([]driver.Value, len(named))
	for i, nv := range named {
		if len(nv.Name) > 0 {
			return nil, errors.New("sql: driver does not support the use of Named Parameters")
		}
		vals[i] = nv.Value
	}
	return vals, nil
}

// valuesToNamedValues adapts the deprecated positional Stmt entry points onto
// the context-aware ones.
func valuesToNamedValues(vals []driver.Value) []driver.NamedValue {
	named := make([]driver.NamedValue, len(vals))
	for i, v := range vals {
		named[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return named
}

// namedValuesToAny copies bound arguments into the event payload.
func namedValuesToAny(named []driver.NamedValue) []any {
	if len(named) == 0 {
		return nil
	}
	out := make([]any, len(named))
	for i, nv := range named {
		out[i] = nv.Value
	}
	return out
}
