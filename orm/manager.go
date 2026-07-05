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

	"github.com/velocitykode/velocity/events"
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
	LogQueries      bool
	SlowThreshold   time.Duration
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
	connections   map[string]drivers.Driver
	defaultName   string
	databaseName  string
	// closed flips to true at the start of Shutdown, before any driver
	// is closed, so racing queries fail fast with ErrManagerShutdown
	// instead of surfacing database/sql closed-connection noise (or nil
	// dereferences). Atomic so execution hot paths can check liveness
	// without taking mu.
	closed atomic.Bool
	// eventDispatcher is the typed event handler invoked by dispatchEvent.
	// SetEventDispatcher (deprecated, untyped) adapts the legacy signature
	// into a typed call so internal event firing remains type-safe.
	eventDispatcher func(ctx context.Context, event Event) error
	// rawEventDispatcher is the untyped dispatcher set via SetEventDispatcher.
	// It is the legacy flush sink for KindDispatch / KindDispatchNow buffered
	// entries; richer kinds (Async / After / Until) prefer txEventBus when
	// it is wired so listener semantics like ShouldQueue and the original
	// delay are preserved across the transactional buffer boundary.
	rawEventDispatcher func(ctx context.Context, event any) error
	// txEventBus, when non-nil, is the kind-aware sink for buffered
	// entries flushed at commit. It is wired by velocity.bootstrap so the
	// per-transaction events.BufferedDispatcher can route entries back
	// through the matching method on the underlying dispatcher.
	txEventBus events.Dispatcher
	// logger receives warnings about runtime conditions (transaction
	// rollback failures, recovered panics). nil until SetLogger is called.
	logger eventLogger
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
	connConfig.SlowQueryThreshold = config.SlowThreshold

	driver, err := driverRegistry.Resolve(ctx, config.Driver, connConfig)
	if err != nil {
		return nil, fmt.Errorf("velocity/orm: %w", err)
	}

	m := &Manager{
		defaultDriver: driver,
		connections:   make(map[string]drivers.Driver),
		defaultName:   config.Driver,
		databaseName:  config.Database,
	}

	return m, nil
}

// DB returns the underlying *sql.DB from the default connection.
func (m *Manager) DB() *sql.DB {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil
	}
	return m.defaultDriver.DB()
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

// AddConnection registers a named database connection.
func (m *Manager) AddConnection(name string, driver drivers.Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connections[name] = driver
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
			return nil, fmt.Errorf("orm: raw query failed: %w", err)
		}
		return rows, nil
	}

	driver, err := m.liveDriver()
	if err != nil {
		return nil, err
	}
	rows, err := driver.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("orm: raw query failed: %w", err)
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
			return nil, fmt.Errorf("orm: exec failed: %w", err)
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
	m.mu.RLock()
	logger := m.logger
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

	// Mint a fresh span for the tx body so every QueryExecuted event running
	// under fn parents under this span. The pre-existing tx span (if any)
	// becomes the ParentID for nested Transaction calls, letting an APM
	// exporter render nested transactions as a tree. For top-level calls the
	// caller's span is the parent. Per-statement events read parentID from
	// txCtx, so we install txSpanID as the parent on the body ctx and a fresh
	// stmtRoot as the body's own span.
	//
	// Statements emitted under txTraceCtx increment txStmtCounter via
	// dispatchQueryExecuted; the count ships on the TransactionExecuted event.
	parentSpanID := txSpanIDFromContext(ctx)
	if parentSpanID == "" {
		parentSpanID = trace.GetSpanID(ctx)
	}
	txTrace := trace.GetTraceID(ctx)
	if txTrace == "" {
		// Use Must* helpers: a transaction must not fail because of a
		// transient entropy outage. On rand failure the IDs degrade to
		// the distinguishable fallback markers (not all-zero hex), so
		// downstream APM cannot conflate fake with real traces.
		txTrace = trace.MustGenerateTraceID()
	}
	txSpanID := trace.MustGenerateSpanID()
	stmtRootSpan := trace.MustGenerateSpanID()
	txTraceCtx := trace.WithFullContext(ctx, txTrace, stmtRootSpan, txSpanID)
	txTraceCtx = withTxSpanID(txTraceCtx, txSpanID)
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
	// ShouldQueue and the recorded delay survive the buffer boundary.
	// When a richer events.Dispatcher is wired (the production path) we
	// dispatch through it; otherwise we fall back to the untyped legacy
	// sink, which collapses every kind onto Dispatch.
	buffer, releaseBuffer := events.InstallBuffer(ctx, func(entry events.BufferedEvent) error {
		return flushBufferedEntry(ctx, entry, bus, rawDispatcher)
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
	dispatcher := func(ev *TxRecover) { m.dispatchEvent(ctx, ev) }
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

	dispatchTxExecuted := func(errMsg string) {
		m.dispatchEvent(ctx, &TransactionExecuted{
			Context:    ctx,
			Connection: connName,
			Duration:   time.Since(txStart),
			Statements: int(txStmtCounter.Load()),
			Error:      errMsg,
			TraceID:    txTrace,
			SpanID:     txSpanID,
			ParentID:   parentSpanID,
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
				// Surface rollback failure through the configured logger
				// when available; otherwise fire a typed event so callers
				// with a dispatcher wired up still observe the failure.
				if logger != nil {
					logger.Error("velocity/orm: rollback failed after panic", "error", rbErr, "panic", fmt.Sprint(p))
				}
				m.dispatchEvent(ctx, &TxRecover{
					Cause:       "panic",
					PanicValue:  fmt.Sprint(p),
					RollbackErr: rbErr.Error(),
				})
			}
			dispatchTxExecuted(fmt.Sprintf("panic: %v", p))
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
			if logger != nil {
				logger.Error("velocity/orm: rollback failed", "error", rbErr, "original_error", err)
			}
			m.dispatchEvent(ctx, &TxRecover{
				Cause:       "error",
				OriginalErr: err.Error(),
				RollbackErr: rbErr.Error(),
			})
		}
		dispatchTxExecuted(err.Error())
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
		dispatchTxExecuted(cmErr.Error())
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
		dispatchTxExecuted("")
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
		dispatchTxExecuted("")
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
		dispatchTxExecuted("")
		drainOnCommit()
		return acErr
	}
	dispatchTxExecuted("")
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

// Shutdown closes the default database connection and all named connections,
// honoring the context deadline.
func (m *Manager) Shutdown(ctx context.Context) error {
	// Mark closed before touching any driver (and before taking mu) so
	// concurrent queries observe the shutdown immediately and return
	// ErrManagerShutdown rather than hitting a half-closed pool.
	m.closed.Store(true)

	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	if m.defaultDriver != nil {
		if err := m.defaultDriver.Close(); err != nil {
			firstErr = err
		}
		m.defaultDriver = nil
	}

	for name, conn := range m.connections {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.connections, name)
	}

	return firstErr
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return ""
	}
	return m.defaultDriver.DriverName()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.eventDispatcher = nil
		m.rawEventDispatcher = nil
		return
	}
	m.rawEventDispatcher = fn
	m.eventDispatcher = func(ctx context.Context, event Event) error {
		return fn(ctx, event)
	}
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

// eventLogger is the minimal logger contract the manager uses to report
// runtime conditions (e.g. failed rollback). It matches the shape of
// log.Logger without importing the package, keeping orm a leaf dependency.
type eventLogger interface {
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
}

// SetLogger installs a logger that receives warnings about recovered
// transaction panics and failed rollbacks. Callers may pass any value
// satisfying the Warn/Error shape (typically log.Logger).
func (m *Manager) SetLogger(logger eventLogger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logger = logger
}

// dispatchEvent dispatches an event if a dispatcher is configured. ctx
// reaches every listener so trace IDs and request-scoped values flow
// through; cancellation/deadline behavior depends on the dispatcher.
func (m *Manager) dispatchEvent(ctx context.Context, event Event) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		_ = fn(ctx, event)
	}
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
