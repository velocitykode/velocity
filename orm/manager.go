package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/drain"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/eventmeta"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/internal/sqlerr"
	"github.com/velocitykode/velocity/internal/teardown"
	"github.com/velocitykode/velocity/orm/drivers"
	"github.com/velocitykode/velocity/trace"
)

// ManagerConfig holds typed configuration for creating an ORM Manager.
type ManagerConfig struct {
	Driver   string
	Host     string
	Port     string
	Database string
	Username string
	Password string
	Charset  string
	SSLMode  string // postgres
	TLS      string // mysql
	// TimeZone is the database SESSION timezone (postgres `TimeZone=`,
	// mysql `time_zone='...'`). It affects in-database functions and
	// timestamptz/TIMESTAMP rendering only, never the encoding of bound
	// time values (storage is unconditionally UTC). Unused on SQLite.
	TimeZone        string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	// LogQueries writes one debug line per executed statement through the
	// manager's logger: the statement and its argument count, never the
	// argument values (see drivers.BaseDriver.SetLogger). The line is
	// written before the statement's connection returns to the pool, so
	// the manager's logger must not use the pool it logs.
	LogQueries bool
	// SlowThreshold makes a completed statement that ran longer than it
	// write one warn line through the manager's logger and marks its
	// QueryExecuted Slow. Zero disables the rule.
	SlowThreshold time.Duration
	// Logger is the manager's logger from construction on, as if SetLogger
	// had installed it before the driver connected: the statements the
	// driver runs while it connects write their query log lines to it.
	// Nil means the framework's standalone fallback logger until SetLogger
	// installs one.
	Logger contract.Logger
}

// Database is the interface satisfied by *Manager. It covers the methods used
// through app.Services and router.Context for query execution, transactions,
// connection management, and event wiring.
//
// Transaction takes a closure that receives the per-tx context. The
// returned ctx carries a *sql.Tx that any ORM terminal which observes
// it (every read and write entry point takes ctx as its first
// positional argument) automatically participates in. There is no
// per-call WithTx decoration; mixing tx-aware and tx-unaware writes
// inside a single closure is impossible without the caller explicitly
// opting out by passing a non-tx ctx. Callers who need the raw
// *sql.Tx (e.g. for SAVEPOINT issuance) extract it via TxFromContext.
type Database interface {
	DB() *sql.DB
	Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
	Begin(ctx context.Context) (*sql.Tx, error)
	Shutdown(ctx context.Context) error
	Ping() error
	DriverName() string
	DatabaseName() string
	Stats() sql.DBStats
	DefaultDriver() drivers.Driver
	Connection(name string) (drivers.Driver, error)
	AddConnection(name string, driver drivers.Driver)
	// SetEventDispatcher wires the event dispatcher used by ORM internals
	// to surface query and transaction lifecycle events. The fn receives
	// ctx so listeners observe request- / tx-scoped values.
	SetEventDispatcher(fn func(ctx context.Context, event any) error)
}

// Verify *Manager implements Database at compile time.
var _ Database = (*Manager)(nil)

// Manager manages database connections. It is the instance-based alternative
// to the package-level global functions.
type Manager struct {
	mu            sync.RWMutex
	defaultDriver drivers.Driver
	// shutDefault is the default connection Shutdown took out of
	// defaultDriver to close. It is kept for its identity alone, so a
	// registration of that instance once Shutdown began is not closed a
	// second time. Guarded by mu.
	shutDefault  drivers.Driver
	connections  map[string]drivers.Driver
	defaultName  string
	databaseName string
	// closed flips to true at the start of Shutdown, before any driver
	// is closed, so racing queries fail fast with ErrManagerShutdown
	// instead of surfacing database/sql closed-connection noise (or nil
	// dereferences). Atomic so execution hot paths can check liveness
	// without taking mu.
	closed atomic.Bool
	// events holds the dispatcher dispatchEvent hands events to and
	// applies the failure policy to a failed dispatch, a listener panic the
	// statement-event pump recovered, and a statement event the pump
	// dropped (see internal/eventemit). Set only under mu, with the
	// pump/hasDispatcher transition; read lock-free.
	events eventemit.Emitter
	// hasDispatcher mirrors "a dispatcher is installed" for the statement
	// observation fast path, which runs inside a driver callback and must
	// not take mu.
	hasDispatcher atomic.Bool
	// pump delivers statement events off the goroutine that ran the query.
	// Created on the first SetEventDispatcher with a non-nil dispatcher;
	// nil until then, so a manager nobody listens to owns no goroutine.
	pump atomic.Pointer[eventPump]
	// rawEventDispatcher is the untyped dispatcher set via SetEventDispatcher.
	// It is the legacy flush sink for KindDispatch / KindDispatchNow buffered
	// entries; richer kinds (Async / After / Until) prefer txEventBus when
	// it is wired so listener semantics like the Async() queue opt-in and
	// the original delay are preserved across the transactional buffer
	// boundary.
	rawEventDispatcher func(ctx context.Context, event any) error
	// txEventBus, when non-nil, is the kind-aware sink for buffered
	// entries flushed at commit. It is wired by velocity.bootstrap so the
	// per-transaction events.BufferedDispatcher can route entries back
	// through the matching method on the underlying dispatcher.
	txEventBus events.Dispatcher
	// logger forwards to the logger the manager writes through (warnings
	// about transaction rollback failures, recovered panics) and is the one
	// query logger every connection that takes one holds. Its target is
	// swapped atomically by SetLogger; unset, it forwards to the
	// framework's standalone fallback logger.
	logger fallbacklog.Forwarder
	// unhanded holds the connections that take a logger
	// (contract.LoggerAware) but have not been handed the forwarder yet,
	// because the manager had no logger when they were added: such a
	// connection keeps a logger of its own until the first SetLogger.
	// Keyed by connection name; the default connection is
	// unhandedDefault. Guarded by mu, with the target change that drains
	// them, so a connection is handed the forwarder exactly once.
	unhanded        map[string]contract.LoggerAware
	unhandedDefault contract.LoggerAware
	// shutdowns is the named connections' lifecycle: AddConnection retires
	// through it a connection it displaces, and Shutdown awaits those
	// retirements. Called under mu.
	shutdowns teardown.Children[drivers.Driver]
	// own and run drain the manager's work in flight at Shutdown
	// (internal/drain): the statement-event pump's two goroutines are
	// units admitted into run, and Shutdown's own work (the pump's drain,
	// then the drivers' closes) runs as the owner's work, so a Shutdown
	// called back from a listener or a driver's Close is refused instead
	// of waiting on itself. run is made under mu by the first pump start
	// or Shutdown (a manager built as a literal works too); the manager
	// does not start again after it.
	own drain.Owner
	run *drain.Run
}

// NewManager creates a new ORM Manager with a connected database driver.
// The driver name is resolved through the canonical driver registry; any
// third-party driver registered via orm.Drivers().Register is available
// alongside the built-in sqlite, postgres, and mysql backends.
func NewManager(config ManagerConfig) (*Manager, error) {
	return NewManagerWithContext(context.Background(), config)
}

// NewManagerWithContext is the context-aware variant of NewManager. The
// ctx is forwarded to the driver factory so drivers performing network
// I/O during Connect can honour deadlines.
func NewManagerWithContext(ctx context.Context, config ManagerConfig) (*Manager, error) {
	connConfig := drivers.ConnectionConfig{
		Driver:   config.Driver,
		Host:     stringOrDefault(config.Host, "localhost"),
		Port:     stringOrDefault(config.Port, getDefaultPort(config.Driver)),
		Database: config.Database,
		Username: config.Username,
		Password: config.Password,
		Charset:  stringOrDefault(config.Charset, "utf8mb4"),
		SSLMode:  config.SSLMode,
		TLS:      config.TLS,
		TimeZone: config.TimeZone,
	}

	if config.MaxIdleConns > 0 {
		connConfig.MaxIdleConns = config.MaxIdleConns
	} else {
		connConfig.MaxIdleConns = 10
	}

	if config.MaxOpenConns > 0 {
		connConfig.MaxOpenConns = config.MaxOpenConns
	} else {
		connConfig.MaxOpenConns = 100
	}

	if config.ConnMaxLifetime > 0 {
		connConfig.ConnMaxLifetime = config.ConnMaxLifetime
	} else {
		connConfig.ConnMaxLifetime = 3600 * time.Second
	}

	connConfig.LogQueries = config.LogQueries
	connConfig.SlowThreshold = config.SlowThreshold

	m := &Manager{
		connections:  make(map[string]drivers.Driver),
		defaultName:  config.Driver,
		databaseName: config.Database,
	}
	// The forwarder is the connection's logger from its first statement
	// (SQLite's connect-time PRAGMAs included) when the config names one.
	if config.Logger != nil {
		m.logger.Set(config.Logger)
		connConfig.Logger = &m.logger
	}

	driver, err := driverRegistry.Resolve(ctx, config.Driver, connConfig)
	if err != nil {
		return nil, errchain.Errorf("velocity/orm: %w", err)
	}
	// m is not shared yet, so the extension calls run before it is.
	m.attachStatementObserver(driver)
	if la, ok := driver.(contract.LoggerAware); ok {
		if config.Logger != nil {
			la.SetLogger(&m.logger)
		} else {
			m.unhandedDefault = la
		}
	}
	m.defaultDriver = driver

	return m, nil
}

// attachStatementObserver points a driver's pool at this manager, so the
// statements it executes dispatch through this manager's event dispatcher.
// Drivers that did not open an instrumented pool do not implement the
// interface and are skipped.
//
// Binding per pool (rather than resolving a process-wide default at dispatch
// time) is what makes a manager's telemetry its own: a manager constructed
// without SetDefault still reports, and two managers never cross-dispatch.
// A driver handed to two managers reports to whichever attached last.
func (m *Manager) attachStatementObserver(d drivers.Driver) {
	if obs, ok := d.(drivers.StatementObservable); ok {
		obs.SetStatementObserver(managerObserver{m: m})
	}
}

// DB returns the underlying *sql.DB from the default connection.
func (m *Manager) DB() *sql.DB {
	// The driver is called after the lock is released: a driver is user
	// code and may call back into the manager.
	d := m.DefaultDriver()
	if d == nil {
		return nil
	}
	return d.DB()
}

// Connection returns a named database connection.
func (m *Manager) Connection(name string) (drivers.Driver, error) {
	// The closed check runs first: Shutdown also empties m.connections,
	// and "shut down" (fix lifecycle ordering) is the more specific
	// diagnosis than "not found" (fix connection config).
	if m.closed.Load() {
		return nil, ErrManagerShutdown
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	driver, exists := m.connections[name]
	if !exists {
		return nil, fmt.Errorf("orm: connection %s not found", name)
	}
	return driver, nil
}

// AddConnection registers a named database connection. Statements executed
// against it dispatch through this manager's event dispatcher, and the
// manager's logger, once it has one (ManagerConfig.Logger or SetLogger),
// becomes the driver's query logger: the driver is handed the manager's
// forwarding logger once, and every later SetLogger reaches it through
// that. A driver added while the manager has no logger keeps its own until
// the first SetLogger. The driver's SetStatementObserver and SetLogger run
// before the connection is published and under no manager lock, so either
// may call back into the manager. A connection added once Shutdown has
// begun is not published: it is closed, and a warning says so, unless the
// manager still owns it (its default connection, one it holds under a
// name, or one it is closing), which Shutdown closes. A
// connection AddConnection displaces under the same name is closed before
// it returns: contained, once, unless the manager still holds it under
// another name or as its default connection, which Shutdown closes; the
// call succeeds, so a failure to close it is written once as a warning. A
// driver value must be comparable with == (pointer types are): one that is
// not, registered under a second name or also the default, cannot be
// recognised as the same instance and is closed once per registration. A
// configuration call, not concurrent with queries on the displaced
// connection.
func (m *Manager) AddConnection(name string, driver drivers.Driver) {
	m.attachStatementObserver(driver)
	la, aware := driver.(contract.LoggerAware)
	handed := aware && m.logger.Installed() != nil
	if handed {
		la.SetLogger(&m.logger)
	}
	m.mu.Lock()
	if m.closed.Load() {
		// Shutdown has begun: it takes the connections to close under mu
		// after setting closed under mu, so one published now would never
		// be closed. Publish nothing and close it here instead, unless
		// the manager owns it, which that Shutdown closes.
		owned := m.ownsLocked(driver)
		m.mu.Unlock()
		if owned {
			m.warnClose("velocity/orm: connection added after Shutdown is one the manager closes, not added", name, nil)
			return
		}
		m.closeUnpublished(name, driver)
		return
	}
	old := m.connections[name]
	m.connections[name] = driver
	// The default connection is not in the registry Retire looks at: a
	// displaced alias of it stays open, and Shutdown closes it.
	var retire func() error
	if !teardown.SameInstance(old, m.defaultDriver) {
		retire = m.shutdowns.Retire(m.connections, old)
	}
	delete(m.unhanded, name)
	// A SetLogger that ran since the check above left no pending entry for
	// this connection: hand it here, once, after the lock is released.
	late := aware && !handed && m.logger.Installed() != nil
	if aware && !handed && !late {
		if m.unhanded == nil {
			m.unhanded = make(map[string]contract.LoggerAware)
		}
		m.unhanded[name] = la
	}
	m.mu.Unlock()
	if late {
		m.handLogger(la)
	}
	if retire == nil {
		return
	}
	if err := retire(); err != nil {
		m.warnClose("velocity/orm: a displaced connection failed to close", name, err)
	}
}

// ownsLocked reports whether driver is an instance the manager closes: its
// default connection (also once Shutdown took it), one held under a name,
// or one that left the registry and whose close has not returned. The
// caller holds mu.
func (m *Manager) ownsLocked(driver drivers.Driver) bool {
	if teardown.SameInstance(driver, m.defaultDriver) || teardown.SameInstance(driver, m.shutDefault) {
		return true
	}
	for _, held := range m.connections {
		if teardown.SameInstance(held, driver) {
			return true
		}
	}
	for _, held := range m.shutdowns.Closing() {
		if teardown.SameInstance(held, driver) {
			return true
		}
	}
	return false
}

// closeUnpublished closes a connection AddConnection was handed after
// Shutdown began, and writes one warning saying so. The driver's Close is
// user code: a panic in it is contained and reported on the same line.
func (m *Manager) closeUnpublished(name string, driver drivers.Driver) {
	m.warnClose("velocity/orm: connection added after Shutdown was closed, not added", name, teardown.Close(context.Background(), driver))
}

// warnClose writes msg once for the connection name, with the kind of the
// close error err, when there is one: the kind, never the driver's text,
// which may carry the connection's credentials.
func (m *Manager) warnClose(msg, name string, err error) {
	kvs := []any{"connection", name}
	if err != nil {
		kvs = append(kvs, sqlerr.Key, sqlerr.Kind(err))
	}
	fallbacklog.Write(m.log(), func(l contract.Logger) {
		l.Warn(msg, kvs...)
	})
}

// Introspector returns the schema introspector for the default connection.
func (m *Manager) Introspector() (drivers.SchemaIntrospector, error) {
	driver, err := m.liveDriver()
	if err != nil {
		return nil, err
	}
	introspector, ok := driver.(drivers.SchemaIntrospector)
	if !ok {
		return nil, fmt.Errorf("orm: driver %s does not support schema introspection", driver.DriverName())
	}
	return introspector, nil
}

// ConnectionIntrospector returns the schema introspector for a named connection.
func (m *Manager) ConnectionIntrospector(name string) (drivers.SchemaIntrospector, error) {
	if m.closed.Load() {
		return nil, ErrManagerShutdown
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	driver, exists := m.connections[name]
	if !exists {
		return nil, fmt.Errorf("orm: connection %s not found", name)
	}
	introspector, ok := driver.(drivers.SchemaIntrospector)
	if !ok {
		return nil, fmt.Errorf("orm: driver %s does not support schema introspection", driver.DriverName())
	}
	return introspector, nil
}

// ListTables returns user tables from the default connection.
func (m *Manager) ListTables(ctx context.Context) ([]string, error) {
	introspector, err := m.Introspector()
	if err != nil {
		return nil, err
	}
	return introspector.ListTables(ctx)
}

// DescribeTable returns column metadata for a table on the default connection.
func (m *Manager) DescribeTable(ctx context.Context, table string) ([]drivers.ColumnSchema, error) {
	introspector, err := m.Introspector()
	if err != nil {
		return nil, err
	}
	return introspector.DescribeTable(ctx, table)
}

// ListTablesOn returns user tables from a named connection.
func (m *Manager) ListTablesOn(ctx context.Context, name string) ([]string, error) {
	introspector, err := m.ConnectionIntrospector(name)
	if err != nil {
		return nil, err
	}
	return introspector.ListTables(ctx)
}

// DescribeTableOn returns column metadata for a table on a named connection.
func (m *Manager) DescribeTableOn(ctx context.Context, name, table string) ([]drivers.ColumnSchema, error) {
	introspector, err := m.ConnectionIntrospector(name)
	if err != nil {
		return nil, err
	}
	return introspector.DescribeTable(ctx, table)
}

// Raw executes a raw SQL query and returns the resulting rows.
//
// WARNING: The caller is responsible for preventing SQL injection by using
// parameterized queries. Never concatenate user input into the query string.
func (m *Manager) Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	// Fail fast once shutdown has begun, even on the tx path: the
	// underlying pool is closing, so surfacing the ordering mistake
	// beats database/sql closed-connection noise.
	if m.closed.Load() {
		return nil, ErrManagerShutdown
	}
	// Honor a tx carried in ctx (set by Manager.Transaction or
	// WithTxContext): route the read through the tx so it observes
	// uncommitted writes from the same transaction, mirroring how the
	// ORM terminals enroll via bindTxFromContextValue. Without this a
	// raw read inside a transaction (e.g. a test using the
	// transaction-rollback helper) would escape to the pool and miss
	// the transaction's own writes.
	if tx, ok := TxFromContext(ctx); ok {
		// This branch hits *sql.Tx directly, bypassing the driver
		// interface where time args are normally rebased to UTC, so
		// normalize here too (storage contract: instants stored UTC).
		rows, err := tx.QueryContext(ctx, query, drivers.NormalizeTimeArgs(args)...)
		if err != nil {
			return nil, errchain.Errorf("orm: raw query failed: %w", err)
		}
		return rows, nil
	}

	driver, err := m.liveDriver()
	if err != nil {
		return nil, err
	}
	rows, err := driver.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errchain.Errorf("orm: raw query failed: %w", err)
	}
	return rows, nil
}

// Exec executes a raw SQL statement.
//
// WARNING: The caller is responsible for preventing SQL injection by using
// parameterized queries. Never concatenate user input into the query string.
func (m *Manager) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	// Fail fast once shutdown has begun; see Raw for rationale.
	if m.closed.Load() {
		return nil, ErrManagerShutdown
	}
	// Honor a tx carried in ctx (set by Manager.Transaction or
	// WithTxContext): route the write through the tx so it participates in
	// the caller's transaction instead of auto-committing on the pool.
	// Without this a raw Exec inside a transaction (e.g. a test using the
	// transaction-rollback helper) would escape and never roll back.
	if tx, ok := TxFromContext(ctx); ok {
		// Direct *sql.Tx path bypasses the driver interface; rebase
		// time args to UTC here as well.
		res, err := tx.ExecContext(ctx, query, drivers.NormalizeTimeArgs(args)...)
		if err != nil {
			return nil, errchain.Errorf("orm: exec failed: %w", err)
		}
		return res, nil
	}

	driver, err := m.liveDriver()
	if err != nil {
		return nil, err
	}
	return driver.ExecContext(ctx, query, args...)
}

// Transaction executes fn inside a database transaction with the
// per-tx context propagated to the closure.
//
// The ctx passed into fn carries a *sql.Tx that any ORM terminal which
// observes it (every read and write entry point on Query[T] and
// Model[T]/UUIDModel[T]/etc takes ctx as its first positional argument)
// automatically participates in. There is no per-call WithTx
// decoration; mixing tx-aware and tx-unaware ORM writes inside a
// single closure is impossible without the caller explicitly opting
// out by passing a non-tx ctx.
//
// Example:
//
//	err := m.Transaction(ctx, func(ctx context.Context) error {
//	    if _, err := (User{}).Create(ctx, map[string]any{
//	        "name": "alice",
//	    }); err != nil {
//	        return err
//	    }
//	    return Save(ctx, nil, &Audit{Message: "created"})
//	})
//
// Callers who need raw *sql.Tx access (e.g. for SAVEPOINT issuance,
// or to integrate non-ORM SQL helpers) extract it inside the closure
// via TxFromContext(ctx).
//
// Lifecycle:
//   - fn returning a non-nil error rolls back and returns the error.
//   - fn panicking rolls back and re-panics; rollback failures are
//     logged and surfaced via TxRecover events.
//   - fn returning nil commits and flushes any per-tx event buffer.
//
// A per-transaction events.BufferedDispatcher is installed on the
// incoming ctx so callers can record domain events via
// events.Buffer(ctx).Dispatch(...) and have them fire only on commit.
// Nested Transaction calls reuse the outermost buffer (savepoint
// semantics): inner rollback drops only events emitted within the
// inner scope, outer commit flushes the rest.
//
// Concurrency: *sql.Tx is single-threaded by stdlib contract; the ctx
// (and any chain rooted in it) must be used from the goroutine that
// owns the tx. Fanout inside fn must serialize back to one goroutine
// before touching tx-aware ORM helpers.
func (m *Manager) Transaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if fn == nil {
		return nil
	}

	driver, err := m.liveDriver()
	if err != nil {
		return err
	}
	logger := m.log()
	m.mu.RLock()
	rawDispatcher := m.rawEventDispatcher
	bus := m.txEventBus
	m.mu.RUnlock()

	// Savepoint nesting: when ctx already carries a *sql.Tx (set by an
	// outer Manager.Transaction, or by WithTxContext - e.g. the test
	// transaction-rollback helper), nest as a SAVEPOINT on that tx
	// instead of opening a second real transaction on the pool. A second
	// pool transaction would commit independently (escaping the outer
	// rollback) and, with a single-connection pool, deadlock waiting for
	// the connection the outer tx holds.
	parentTx, nested := TxFromContext(ctx)
	var tx *sql.Tx
	var savepoint string
	if nested {
		tx = parentTx
		savepoint = nextSavepointName()
		if _, spErr := tx.ExecContext(ctx, "SAVEPOINT "+savepoint); spErr != nil {
			return spErr
		}
	} else {
		var beginErr error
		tx, beginErr = driver.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
	}

	// commit / rollback primitives differ for the savepoint path: RELEASE
	// SAVEPOINT commits the nested scope; ROLLBACK TO + RELEASE discards
	// it; the parent transaction stays open either way. For a real
	// (non-nested) transaction these are plain Commit / Rollback.
	doCommit := func() error {
		if nested {
			_, e := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
			return e
		}
		return tx.Commit()
	}
	doRollback := func() error {
		if nested {
			if _, e := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT "+savepoint); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "RELEASE SAVEPOINT "+savepoint)
			return e
		}
		return tx.Rollback()
	}

	// The transaction is its own span under the caller's span (a root span
	// when the caller has no trace; a nested Transaction's caller span is
	// the outer tx span), and the body runs under it: every statement in fn
	// is a span of its own whose ParentID is the tx span, and a nested
	// Transaction parents its tx span under this one. ContinueTrace uses
	// the Must* generators, so a transient entropy outage never fails a
	// transaction; the ids degrade to the distinguishable fallback markers.
	//
	// Statements emitted under txTraceCtx increment txStmtCounter as the
	// statement observer records them - synchronously, even though their
	// events are delivered later - so the count ships accurately on the
	// TransactionExecuted event.
	txTraceCtx, txSpanID := trace.ContinueTrace(ctx)
	txTrace, parentSpanID := trace.GetTraceID(txTraceCtx), trace.GetParentID(txTraceCtx)
	txStmtCounter := &atomic.Int32{}
	txTraceCtx = withTxStatementCounter(txTraceCtx, txStmtCounter)
	txStart := time.Now()
	connName := driver.DriverName()

	// Attach a per-transaction buffer so user code can record domain
	// events that fire only on commit. The buffer slot is reachable from
	// the caller's incoming ctx when events.PrepareBuffer(ctx) was used
	// to create it, so events.Buffer(ctx) inside fn finds it without
	// requiring fn to receive a derived ctx for that purpose. Nested
	// Transaction calls reuse the outermost buffer (see
	// events.InstallBuffer for nested savepoint semantics).
	//
	// The flush callback routes each entry through the dispatcher method
	// the caller originally requested (Dispatch / DispatchNow /
	// DispatchAsync / DispatchAfter / Until) so listener semantics like
	// the Async() queue opt-in and the recorded delay survive the buffer
	// boundary.
	// When a richer events.Dispatcher is wired (the production path) we
	// dispatch through it; otherwise we fall back to the untyped legacy
	// sink, which collapses every kind onto Dispatch.
	//
	// A failed flush dispatch goes to the manager's failure policy (shared
	// with the app's), so it is counted, logged and handed to the hook like
	// any failed event; Fail skips an error the dispatch function already
	// recorded, so nothing is counted twice. The error still reaches the
	// transaction's caller.
	buffer, releaseBuffer := events.InstallBuffer(ctx, func(entry events.BufferedEvent) error {
		err := flushBufferedEntry(ctx, entry, bus, rawDispatcher)
		m.events.Fail(ctx, err, entry.Event())
		return err
	})
	defer releaseBuffer()

	// Install per-tx callbacks holder so OnCommit / OnRollback /
	// OnCommitFailure registrations made inside fn (or by model
	// AfterCommit hooks during nested Saves) accumulate against this
	// transaction.
	//
	// owner is true only for the outermost Transaction in a nested
	// chain. Nested calls reuse the outer's callbacks list and
	// defer the drain to the outer wrapper. When ctx has no holder,
	// installTxCallbacks returns a standalone list with owner=true
	// and a no-op release so the contract works even when callers
	// forget PrepareTxCallbacks.
	callbacks, owner, releaseCallbacks := installTxCallbacks(ctx)
	defer releaseCallbacks()

	// Wire the dispatcher so a hook panic surfaces a TxRecover event
	// even when no logger is configured. Only the owner sets this:
	// the outer Transaction owns the drain and therefore owns the
	// dispatcher binding for the entire callback list lifecycle.
	dispatcher := func(build func() *TxRecover) { m.events.EmitBuilt(ctx, func() any { return build() }) }
	if owner {
		callbacks.setDispatcher(dispatcher)
	}
	// Stamp the dispatcher onto ctx so registerModelAfterCommit's
	// inline (auto-commit) branch can route hook panics through the
	// same TxRecover stream.
	ctx = withTxRecoverDispatcher(ctx, dispatcher)

	// drainOnRollback / drainOnCommit / drainOnCommitFailure are
	// no-ops on nested Transactions: the inner closure does not own
	// the callbacks list, so its commit / rollback boundary is not
	// the boundary the application observes.
	drainOnRollback := func() {
		if owner {
			callbacks.runRollback(ctx, logger)
		}
	}
	drainOnCommit := func() {
		if owner {
			callbacks.runCommit(ctx, logger)
		}
	}
	drainOnCommitFailure := func(commitErr error) {
		if owner {
			callbacks.runCommitFailure(ctx, logger, commitErr)
		}
	}

	// Derive the per-tx ctx that fn receives. Calls inside fn that
	// observe this ctx auto-enroll in tx via bindTxFromContextValue.
	// txTraceCtx already carries the fresh tx span and statement counter,
	// so per-statement events under fn parent under the tx span.
	txCtx := WithTxContext(txTraceCtx, tx)

	// Install the after-commit task queue so a Dispatcher.Dispatch call
	// inside fn that targets a ShouldDispatchAfterCommit listener defers
	// the listener until commit. The outermost Transaction owns the
	// drain; nested Transaction calls inherit the queue via context
	// propagation and InstallAfterCommitQueue returns a non-owner handle
	// pinned to the parent queue's current task count. Inner rollback /
	// panic truncates the queue back to that baseline so only
	// inner-enqueued tasks are dropped; outer-enqueued tasks remain.
	// Inner commit is a no-op so forwarding stays owned by the outermost
	// scope. When neither the caller nor the outer Transaction prepared a
	// queue, the outermost call installs one transparently so the gate
	// works without user opt-in.
	var acHandle events.AfterCommitHandle
	txCtx, acHandle = events.InstallAfterCommitQueue(txCtx)
	drainAfterCommit := func() error {
		if acHandle.Owner() {
			return events.FireAfterCommit(txCtx)
		}
		return nil
	}
	// dropAfterCommit handles the four rollback paths (fn error, panic,
	// commit failure, ambiguous commit). On the outer (owner) it nukes
	// the entire queue; on a nested handle it truncates back to the
	// baseline captured at install time so outer-enqueued tasks survive.
	dropAfterCommit := func() {
		if acHandle.Owner() {
			events.DropAfterCommit(txCtx)
			return
		}
		acHandle.TruncateToBaseline()
	}

	// txMeta is the envelope of an event about this transaction: the
	// caller's ctx and the transaction's own span, stamped now.
	txMeta := func() contract.EventMeta {
		return contract.EventMeta{Context: ctx, TraceID: txTrace, SpanID: txSpanID, ParentID: parentSpanID, At: time.Now()}
	}
	dispatchTxExecuted := func(txErr error) {
		m.events.EmitBuilt(ctx, func() any {
			meta := txMeta()
			return &TransactionExecuted{
				EventMeta:  meta,
				Connection: connName,
				Duration:   meta.At.Sub(txStart),
				Statements: int(txStmtCounter.Load()),
				Err:        txErr,
			}
		})
	}
	dispatchTxRecover := func(build func() *TxRecover) {
		m.events.EmitBuilt(ctx, func() any {
			ev := build()
			ev.EventMeta = txMeta()
			return ev
		})
	}

	defer func() {
		if p := recover(); p != nil {
			buffer.Drop()
			// After-commit listeners must never fire on a rolled-back
			// transaction. Drop pending tasks before the panic
			// propagates so a deferred Reportable / outbox enqueue
			// cannot leak side effects.
			dropAfterCommit()
			if rbErr := doRollback(); rbErr != nil {
				// Surface the rollback failure through the logger (the
				// fallback logger without one), and fire a typed event so
				// callers with a dispatcher wired up observe it too.
				// Through fallbacklog.Write: a panicking logger must not
				// replace the panic re-raised below, nor skip the event and
				// the rollback callbacks.
				fields := trace.LogFields(txTraceCtx)
				fallbacklog.Write(logger, func(l contract.Logger) {
					l.With(fields...).Error("velocity/orm: rollback failed after panic", sqlerr.Key, sqlerr.Kind(rbErr), "panic", errchain.Sprint(p))
				})
				dispatchTxRecover(func() *TxRecover {
					return &TxRecover{
						Cause:       "panic",
						PanicValue:  errchain.Sprint(p),
						RollbackErr: rbErr,
					}
				})
			}
			dispatchTxExecuted(panicerr.FromRecovered(p))
			// Drain rollback callbacks before re-panicking. Each
			// callback runs under its own recover so a misbehaving
			// callback cannot mask the original panic value we
			// re-raise below.
			drainOnRollback()
			panic(p)
		}
	}()

	if err := fn(txCtx); err != nil {
		buffer.Drop()
		dropAfterCommit()
		if rbErr := doRollback(); rbErr != nil {
			fields := trace.LogFields(txTraceCtx)
			fallbacklog.Write(logger, func(l contract.Logger) {
				l.With(fields...).Error("velocity/orm: rollback failed", sqlerr.Key, sqlerr.Kind(rbErr), "original_"+sqlerr.Key, sqlerr.Kind(err))
			})
			dispatchTxRecover(func() *TxRecover {
				return &TxRecover{
					Cause:       "error",
					OriginalErr: err,
					RollbackErr: rbErr,
				}
			})
		}
		dispatchTxExecuted(err)
		drainOnRollback()
		return err
	}

	if cmErr := doCommit(); cmErr != nil {
		buffer.Drop()
		// Commit failed: the tx is in an AMBIGUOUS state. The database
		// may have committed but the network failed before the client
		// received the OK, OR the commit may have been outright
		// rejected. Running rollback hooks here would corrupt outboxes
		// (re-enqueue jobs that already fired) or invalidate caches
		// for changes that DID land. Drain ONLY commit-failure
		// callbacks, which receive the commit error so they can
		// branch on driver-specific error codes. After-commit
		// listeners follow the rollback convention (drop on AMBIGUOUS)
		// because firing them on a commit that may not have landed is
		// strictly more dangerous than missing a side effect the
		// operator's commit-failure callbacks can re-trigger.
		dropAfterCommit()
		dispatchTxExecuted(cmErr)
		drainOnCommitFailure(cmErr)
		return cmErr
	}

	// A savepoint RELEASE is not a durable commit boundary: the enclosing
	// real transaction may still roll back. So a nested (savepoint)
	// transaction must NOT flush buffered events, fire after-commit
	// listeners, or run OnCommit callbacks here. Those side effects stay
	// accumulated against the real outer transaction's holders and fire
	// only when it commits; if there is no real outer transaction (e.g. a
	// raw WithTxContext from the transaction-rollback test helper) they
	// correctly never fire, because the data is never durably committed.
	// Inner-scope entries are still discarded on the rollback paths above
	// (buffer.Drop / dropAfterCommit), so this only gates the success path.
	if nested {
		dispatchTxExecuted(nil)
		return nil
	}

	if flushErr := buffer.Flush(); flushErr != nil {
		// The tx itself committed; buffered-event flush failed. Still
		// drain commit callbacks so outbox / cache invalidation runs:
		// the row IS durable, only the in-memory event delivery
		// failed. Surface flushErr to the caller.
		// After-commit listeners still run: the row IS durable so the
		// side effect they encode is legitimate. A listener-level
		// error is reported alongside the buffer flush error so the
		// caller sees both via errors.Is.
		var acErr error
		if owned := drainAfterCommit(); owned != nil {
			acErr = owned
		}
		dispatchTxExecuted(nil)
		drainOnCommit()
		if acErr != nil {
			return errors.Join(flushErr, acErr)
		}
		return flushErr
	}
	if acErr := drainAfterCommit(); acErr != nil {
		// Buffer flush succeeded; an after-commit listener failed.
		// Surface to the caller alongside the normal commit path; the
		// commit callbacks have already been drained on the happy
		// path so we surface the listener error without re-running
		// the callback drain.
		dispatchTxExecuted(nil)
		drainOnCommit()
		return acErr
	}
	dispatchTxExecuted(nil)
	drainOnCommit()
	return nil
}

// savepointCounter generates process-unique savepoint names. A monotonic
// counter guarantees uniqueness among the savepoints active within any single
// transaction (the only scope where collisions would matter).
var savepointCounter atomic.Int64

// nextSavepointName returns a fresh, SQL-safe savepoint identifier. The name is
// generated (never user input) so it needs no quoting and cannot inject.
func nextSavepointName() string {
	return "vsp_" + strconv.FormatInt(savepointCounter.Add(1), 10)
}

// Begin starts a new transaction.
func (m *Manager) Begin(ctx context.Context) (*sql.Tx, error) {
	driver, err := m.liveDriver()
	if err != nil {
		return nil, err
	}
	return driver.BeginTx(ctx, nil)
}

// OwnsCaller reports whether the manager owns the calling goroutine:
// whether the goroutine is running the manager's own work, a
// statement-event listener or the failure hook the event delivery runs,
// or a driver's Close during Shutdown.
// The answer is for this instance only: another manager's work, or
// another app's, is not this one's. App.Shutdown asks it because its
// teardown shuts the manager down and waits for that work, so a Shutdown
// called from the work would wait on itself; it is refused instead.
func (m *Manager) OwnsCaller() bool {
	if p := m.pump.Load(); p != nil && p.onPumpGoroutine() {
		return true
	}
	return m.own.Nested() || m.shutdowns.OwnsCaller()
}

// Shutdown delivers the queued statement events, then closes the default
// database connection and all named connections. Queries issued once it
// began fail with ErrManagerShutdown.
//
// It waits within ctx: at ctx it returns ctx's error while the delivery
// and the closes go on without a bound (the manager forces nothing), and
// every later Shutdown returns the first one's result once they finished.
// A listener that never returns holds the closes, which wait for it.
//
// Called from an event listener or from the failure hook handed a dropped
// event, it returns an error wrapping contract.ErrStopFromOwnWork and
// changes nothing: those run on the goroutines the delivery would wait
// for. Called from a driver's Close
// while the manager closes its drivers, it returns an error wrapping
// contract.ErrStopFromOwnWork while the closes go on. The drivers are
// closed after the manager's lock is released, so a driver's Close may
// call back into the manager.
func (m *Manager) Shutdown(ctx context.Context) error {
	// A pump goroutine can only be running on a pump already published, so
	// this unlocked read cannot miss the one the caller runs on.
	if p := m.pump.Load(); p != nil && p.onPumpGoroutine() {
		return errchain.Errorf("velocity/orm: Shutdown called from a statement-event listener, which the delivery would wait for: %w", contract.ErrStopFromOwnWork)
	}
	if m.own.Nested() || m.shutdowns.OwnsCaller() {
		// The caller is closing one of the manager's connections, work
		// the running Shutdown waits for.
		return errchain.Errorf("velocity/orm: Shutdown called from a driver's Close while the manager closes its drivers: %w", contract.ErrStopFromOwnWork)
	}

	// Mark closed before touching any driver so concurrent queries observe
	// the shutdown immediately and return ErrManagerShutdown rather than
	// hitting a half-closed pool. The mark and the pump snapshot happen
	// together under mu, which SetEventDispatcher holds while it checks
	// closed and publishes a pump: either the pump was published first and
	// is drained below, or SetEventDispatcher sees closed and starts none.
	m.mu.Lock()
	m.closed.Store(true)
	run := m.runLocked()
	p := m.pump.Load()
	m.mu.Unlock()
	return m.own.Stop(ctx, run, func() error { return m.stopWork(run, p) }, nil)
}

// runLocked returns the manager's run, made on first use. The caller
// holds mu.
func (m *Manager) runLocked() *drain.Run {
	if m.run == nil {
		m.run = m.own.NewRun()
	}
	return m.run
}

// stopWork is Shutdown's work, run once: deliver the queued statement
// events and wait for the pump's goroutines to return, then close the
// drivers.
func (m *Manager) stopWork(run *drain.Run, p *eventPump) error {
	// Deliver queued statement events before the dispatcher goes away,
	// outside mu: the delivery runs listeners, which may use the manager.
	// No deadline: a deadline is the waiter's outcome, not the drain's.
	var drainErr error
	if p != nil {
		if err := p.stop(context.Background()); err != nil {
			drainErr = errchain.Errorf("velocity/orm: deliver query events: %w", err)
		}
	}
	<-run.Idle()

	// Take the connections out under mu and close them after it is
	// released: a driver's Close is user code, which may log through the
	// manager's forwarder into a logger that calls back into the manager.
	// The named connections close through their lifecycle (shutdowns),
	// which also awaits the ones AddConnection retired; the default one,
	// set once at construction and not in that registry, closes last,
	// unless a name holds the same instance and closes it already.
	m.mu.Lock()
	defaultDriver := m.defaultDriver
	m.defaultDriver = nil
	m.shutDefault = defaultDriver
	for _, conn := range m.connections {
		if teardown.SameInstance(conn, defaultDriver) {
			defaultDriver = nil
			break
		}
	}
	wait := m.shutdowns.Shutdown(&m.connections, func(name string, err error) error {
		return errchain.Errorf("velocity/orm: close connection %q: %w", name, err)
	})
	m.unhanded, m.unhandedDefault = nil, nil
	m.mu.Unlock()

	// No deadline here either: the closes finish, and the waiters bound
	// their own wait.
	errs := []error{drainErr, teardown.Drain(context.Background(), wait)}
	if defaultDriver != nil {
		errs = append(errs, teardown.Close(context.Background(), defaultDriver))
	}
	return errors.Join(errs...)
}

// Ping verifies the default database connection.
func (m *Manager) Ping() error {
	driver, err := m.liveDriver()
	if err != nil {
		return err
	}
	return driver.Ping()
}

// DefaultDriver returns the default database driver (used internally by model Save).
func (m *Manager) DefaultDriver() drivers.Driver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultDriver
}

// liveDriver returns the default driver, or the sentinel explaining why it
// is unavailable: ErrManagerShutdown after Shutdown, ErrNoConnection when
// no default connection was ever configured. The closed check runs first
// because after Shutdown the driver is also nil, and "shut down" is the
// more specific diagnosis.
func (m *Manager) liveDriver() (drivers.Driver, error) {
	if m.closed.Load() {
		return nil, ErrManagerShutdown
	}
	m.mu.RLock()
	d := m.defaultDriver
	m.mu.RUnlock()
	if d == nil {
		return nil, ErrNoConnection
	}
	return d, nil
}

// DriverName returns the name of the default database driver.
func (m *Manager) DriverName() string {
	d := m.DefaultDriver()
	if d == nil {
		return ""
	}
	return d.DriverName()
}

// DatabaseName returns the name of the current database.
func (m *Manager) DatabaseName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.databaseName
}

// Stats returns database connection pool statistics.
func (m *Manager) Stats() sql.DBStats {
	if db := m.DB(); db != nil {
		return db.Stats()
	}
	return sql.DBStats{}
}

// SetEventDispatcher wires the dispatcher used by ORM internals to surface
// query and transaction lifecycle events. The supplied function receives
// ctx so listeners observe request- / tx-scoped values (trace IDs,
// auth, deadlines).
func (m *Manager) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	// The whole transition - dispatcher fields, pump, and the hasDispatcher
	// flag - happens under mu. Updating the flag outside the lock makes
	// concurrent calls lose updates: a call installing a dispatcher could
	// release mu, be overtaken by a call clearing it, and then stamp the
	// flag back to true. Statements would be recorded against a manager
	// with no dispatcher and silently discarded at delivery.
	m.mu.Lock()
	defer m.mu.Unlock()

	if fn == nil {
		m.events.Set(nil)
		m.rawEventDispatcher = nil
		m.hasDispatcher.Store(false)
		return
	}
	m.rawEventDispatcher = fn
	m.events.Set(fn)
	// Failures log through the manager's logger as it is when they
	// happen. Installed here, not in NewManager, so a manager built as a
	// literal gets it too: nothing reaches the policy before a dispatcher
	// is installed.
	m.events.UseLogger(m.log)

	// A manager already shut down does not get a new pump: its Shutdown has
	// drained and stopped the old one, and starting another would leak the
	// goroutine. Leaving hasDispatcher false keeps the observer off.
	if m.closed.Load() {
		return
	}

	// Start the statement-event pump on first use. Managers that never
	// wire a dispatcher never own a goroutine.
	//
	// Ordering matters: a running pump must exist before hasDispatcher goes
	// true. The observer checks hasDispatcher to decide whether to record a
	// statement and then needs a pump to hand it to, so advertising first
	// would open a window where concurrent statements are observed and then
	// discarded for want of a pump - silent loss outside the documented
	// queue-full drop.
	//
	// Starting the pump while holding mu is safe: start only spawns the
	// delivery goroutine, which blocks on an empty queue and, when it does
	// dispatch, takes mu for reading and so simply waits for this call to
	// return.
	if m.pump.Load() == nil {
		p := newEventPump(m.events.Fail, m.events.FailLater, m.runLocked())
		// The observer built each queued event while a dispatcher was
		// installed (hasDispatcher); the pump hands it over later, off the
		// driver callback.
		p.start(func(ctx context.Context, ev contract.Event) {
			m.events.EmitBuilt(ctx, func() any { return ev })
		})
		m.pump.Store(p)
	}
	m.hasDispatcher.Store(true)
}

// SetTxEventBus wires a kind-aware events.Dispatcher used to drain the
// per-transaction events.BufferedDispatcher on commit. With this set, a
// buffered DispatchAsync / DispatchAfter / Until call routes through the
// matching method on bus instead of collapsing onto Dispatch via the
// legacy untyped dispatcher set by SetEventDispatcher.
//
// Pass nil to clear the binding (the buffered flush then falls back to
// rawEventDispatcher, if any).
func (m *Manager) SetTxEventBus(bus events.Dispatcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txEventBus = bus
}

// flushBufferedEntry routes one buffered entry to the underlying
// dispatcher, preferring the kind-aware bus and falling back to the
// untyped legacy sink so existing wirings (tests, partial bootstraps)
// continue to work. It is exported only via the closure passed to
// events.InstallBuffer; outside callers should not need it.
func flushBufferedEntry(ctx context.Context, entry events.BufferedEvent, bus events.Dispatcher, raw func(context.Context, any) error) error {
	if bus != nil {
		switch entry.Kind() {
		case events.KindDispatch:
			return bus.Dispatch(ctx, entry.Event())
		case events.KindDispatchNow:
			return bus.DispatchNow(ctx, entry.Event())
		case events.KindDispatchAsync:
			return bus.DispatchAsync(ctx, entry.Event())
		case events.KindDispatchAfter:
			return bus.DispatchAfter(ctx, entry.Event(), entry.Delay())
		case events.KindUntil:
			_, err := bus.Until(ctx, entry.Event())
			return err
		default:
			return bus.Dispatch(ctx, entry.Event())
		}
	}
	if raw != nil {
		return raw(ctx, entry.Event())
	}
	return nil
}

// SetLogger installs a logger that receives warnings about recovered
// transaction panics and failed rollbacks, and that every connection's
// driver that takes one (contract.LoggerAware) writes its query log to
// (ManagerConfig.LogQueries, ManagerConfig.SlowThreshold). Nil restores
// the default, the framework's standalone fallback logger, for both (it
// drops the query log's debug lines and writes slow query warnings to
// standard error).
//
// Connections do not hold the installed logger itself: each is handed the
// manager's forwarding logger once, and SetLogger swaps the forwarder's
// target atomically, so a line a connection writes after SetLogger returns
// goes to the new logger. A logger bound from the forwarder (With) follows
// later swaps too. The first SetLogger hands the forwarder to the
// connections that were added while the manager had no logger, after the
// swap and under no manager lock, so their SetLogger may call back into
// the manager. Safe to call concurrently, and while the connections run
// queries: overlapping calls leave the manager and every connection on the
// logger installed last.
func (m *Manager) SetLogger(logger contract.Logger) {
	m.mu.Lock()
	m.logger.Set(logger)
	pending := make([]contract.LoggerAware, 0, len(m.unhanded)+1)
	if m.unhandedDefault != nil {
		pending = append(pending, m.unhandedDefault)
	}
	for _, la := range m.unhanded {
		pending = append(pending, la)
	}
	m.unhanded, m.unhandedDefault = nil, nil
	m.mu.Unlock()

	for _, la := range pending {
		m.handLogger(la)
	}
}

// handLogger hands la the manager's forwarding logger. A driver's
// SetLogger is user code: a panic in it is contained and written as a
// warning, so the drivers after it are still handed the forwarder.
func (m *Manager) handLogger(la contract.LoggerAware) {
	m.logger.Hand(la, "velocity/orm: a connection's SetLogger panicked; it keeps its own logger")
}

// log returns the installed logger, or the framework's standalone fallback
// logger when none is installed. Lock-free.
func (m *Manager) log() contract.Logger {
	return fallbacklog.Resolve(m.logger.Installed())
}

// Logger returns the logger the manager writes through: the one SetLogger
// installed, or the framework's standalone fallback logger. Code that
// reports a failure of work it ran against the manager (the validation
// package's database rules) writes it here, so it lands where the
// manager's own lines do. Safe to call concurrently with SetLogger.
func (m *Manager) Logger() contract.Logger {
	return m.log()
}

var _ contract.LoggerAware = (*Manager)(nil)

// dispatchTxRecover dispatches the TxRecover build returns, for work
// running under ctx's span. The event is built only when a dispatcher is
// installed. ctx reaches every listener so trace IDs and request-scoped
// values flow through; a failed dispatch is counted and its event's first
// failure logged through the manager's logger (see internal/eventemit).
func (m *Manager) dispatchTxRecover(ctx context.Context, build func() *TxRecover) {
	m.events.EmitBuilt(ctx, func() any {
		ev := build()
		ev.EventMeta = eventmeta.Current(ctx)
		return ev
	})
}

// ShareEventFailures is the app's wiring seam for the failure policy: the
// framework calls it at every lifecycle boundary to have the manager record
// its failed event dispatches, the listener panics its statement-event pump
// recovers and the statement events it drops in the app's failed event
// count. Its argument is framework-internal, so nothing outside the
// framework can build one; nil returns the manager to its own count.
func (m *Manager) ShareEventFailures(f *eventemit.Failures) {
	m.events.Share(f)
}

// defaultManager is the framework-level default ORM Manager, set once by
// velocity.New(). Model static methods (Find, Where, All, etc.) resolve their
// database driver from this manager so that application code can write:
//
//	Team{}.Find(id)
//
// without passing a manager explicitly.
var (
	defaultManager *Manager
	defaultMu      sync.RWMutex
)

// SetDefault sets the package-level default Manager. Called by velocity.New()
// after constructing the manager — application code should never call this.
func SetDefault(m *Manager) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultManager = m
}

// Default returns the package-level default Manager, or nil if none is set.
func Default() *Manager {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultManager
}

// ResetDefault clears the default manager. Used in tests to ensure isolation.
func ResetDefault() {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultManager = nil
}

func stringOrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}
