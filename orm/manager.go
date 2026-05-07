package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/orm/drivers"
)

// ManagerConfig holds typed configuration for creating an ORM Manager.
type ManagerConfig struct {
	Driver          string
	Host            string
	Port            string
	Database        string
	Username        string
	Password        string
	Charset         string
	SSLMode         string // postgres
	TLS             string // mysql
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	LogQueries      bool
	SlowThreshold   time.Duration
}

// Database is the interface satisfied by *Manager. It covers the methods used
// through app.Services and router.Context for query execution, transactions,
// connection management, and event wiring.
type Database interface {
	DB() *sql.DB
	Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Transaction(ctx context.Context, fn func(tx *sql.Tx) error) error
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
	// to surface query and transaction lifecycle events.
	SetEventDispatcher(fn func(event any) error)
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
	// eventDispatcher is the typed event handler invoked by dispatchEvent.
	// SetEventDispatcher (deprecated, untyped) adapts the legacy signature
	// into a typed call so internal event firing remains type-safe.
	eventDispatcher func(event Event) error
	// rawEventDispatcher is the untyped dispatcher set via SetEventDispatcher.
	// It is the legacy flush sink for KindDispatch / KindDispatchNow buffered
	// entries; richer kinds (Async / After / Until) prefer txEventBus when
	// it is wired so listener semantics like ShouldQueue and the original
	// delay are preserved across the transactional buffer boundary.
	rawEventDispatcher func(event any) error
	// txEventBus, when non-nil, is the kind-aware sink for buffered
	// entries flushed at commit. It is wired by velocity.bootstrap so the
	// per-transaction events.BufferedDispatcher can route entries back
	// through the matching method on the underlying dispatcher.
	txEventBus events.Dispatcher
	// logger receives warnings about runtime conditions (transaction
	// rollback failures, recovered panics). nil until SetLogger is called.
	logger eventLogger
}

// createDriver instantiates a database driver by name.
func createDriver(name string) (drivers.Driver, error) {
	switch name {
	case "sqlite", "sqlite3":
		return drivers.NewSQLiteDriver(), nil
	case "postgres":
		return drivers.NewPostgresDriver(), nil
	case "mysql":
		return drivers.NewMySQLDriver(), nil
	default:
		return nil, fmt.Errorf("velocity/orm: driver %q not registered: %w", name, ErrDriverNotFound)
	}
}

// NewManager creates a new ORM Manager with a connected database driver.
func NewManager(config ManagerConfig) (*Manager, error) {
	driver, err := createDriver(config.Driver)
	if err != nil {
		return nil, err
	}

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

	if err := driver.Connect(connConfig); err != nil {
		return nil, fmt.Errorf("orm: failed to connect to database: %w", err)
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

// Raw executes a raw SQL query and returns the resulting rows.
//
// WARNING: The caller is responsible for preventing SQL injection by using
// parameterized queries. Never concatenate user input into the query string.
func (m *Manager) Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	rows, err := m.defaultDriver.QueryContext(ctx, query, args...)
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return m.defaultDriver.ExecContext(ctx, query, args...)
}

// Transaction executes a function within a database transaction.
//
// A per-transaction events.BufferedDispatcher is attached to the ctx
// passed into fn so callers can record domain events via
// events.Buffer(ctx).Dispatch(...) and have them fire only on commit.
// Rollback (whether triggered by fn returning an error, or a panic)
// drops the buffered events. Nested Transaction calls reuse the
// outermost buffer (savepoint semantics): inner rollback drops only
// events emitted within the inner scope, outer commit flushes the rest.
func (m *Manager) Transaction(ctx context.Context, fn func(tx *sql.Tx) error) error {
	m.mu.RLock()
	driver := m.defaultDriver
	logger := m.logger
	rawDispatcher := m.rawEventDispatcher
	bus := m.txEventBus
	m.mu.RUnlock()

	if driver == nil {
		return errors.New("velocity/orm: no database connection")
	}

	tx, err := driver.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Attach a per-transaction buffer so user code can record domain
	// events that fire only on commit. The buffer slot is reachable from
	// the caller's incoming ctx when events.PrepareBuffer(ctx) was used
	// to create it, so events.Buffer(ctx) inside fn finds it without
	// requiring fn to receive a derived ctx (the fn signature stays the
	// same). Nested Transaction calls reuse the outermost buffer (see
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
		return flushBufferedEntry(entry, bus, rawDispatcher)
	})
	defer releaseBuffer()

	defer func() {
		if p := recover(); p != nil {
			buffer.Drop()
			if rbErr := tx.Rollback(); rbErr != nil {
				// Surface rollback failure through the configured logger
				// when available; otherwise fire a typed event so callers
				// with a dispatcher wired up still observe the failure.
				if logger != nil {
					logger.Error("velocity/orm: rollback failed after panic", "error", rbErr, "panic", fmt.Sprint(p))
				}
				m.dispatchEvent(&TxRecover{
					Cause:       "panic",
					PanicValue:  fmt.Sprint(p),
					RollbackErr: rbErr.Error(),
				})
			}
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		buffer.Drop()
		if rbErr := tx.Rollback(); rbErr != nil {
			if logger != nil {
				logger.Error("velocity/orm: rollback failed", "error", rbErr, "original_error", err)
			}
			m.dispatchEvent(&TxRecover{
				Cause:       "error",
				OriginalErr: err.Error(),
				RollbackErr: rbErr.Error(),
			})
		}
		return err
	}

	if cmErr := tx.Commit(); cmErr != nil {
		buffer.Drop()
		return cmErr
	}
	return buffer.Flush()
}

// WithTx returns a Manager whose default driver routes data-plane ops
// (Save/Update/Delete and Query reads) through tx instead of the
// connection pool. Use this inside a Transaction closure so ORM writes
// participate in the caller's transaction:
//
//	m.Transaction(ctx, func(tx *sql.Tx) error {
//	    txm := m.WithTx(tx)
//	    return orm.Save(txm, &user)
//	})
//
// Or chain off a model to keep the call site terse:
//
//	User{}.WithTx(tx).Create(map[string]any{...})
//
// The returned Manager shares connections, dispatchers, and logger with
// the receiver so events fired during the tx still flow through the
// configured sinks. Schema and connection-management calls on the
// returned Manager fall back to the wrapped driver, except BeginTx,
// Close, and DB which are disabled on the tx-bound driver:
//   - BeginTx: nesting a transaction is a savepoint; issue SAVEPOINT
//     on the underlying *sql.Tx directly.
//   - Close: would tear down the parent pool that other goroutines
//     still depend on.
//   - DB: returning the parent pool would let callers silently bypass
//     the bound transaction.
//
// Concurrency: *sql.Tx is single-threaded by stdlib contract, and this
// wrapper adds no guard. All ORM calls made via the returned Manager
// (or via Model[T].WithTx / Query[T].WithTx) must originate from the
// same goroutine that owns the transaction. Fanout inside the
// Transaction closure must serialize back to one goroutine before
// touching tx-bound helpers.
//
// Snapshot semantics: the returned Manager captures a snapshot of the
// receiver's drivers, dispatchers, and logger at call time. Subsequent
// AddConnection / SetEventDispatcher / SetTxEventBus / SetLogger calls
// on the parent are not observed by the child. Tx scopes are
// short-lived in practice, so this matches the expected lifecycle;
// callers who need late binding should derive WithTx after the parent
// is fully configured.
func (m *Manager) WithTx(tx *sql.Tx) *Manager {
	m.mu.RLock()
	defaultDriver := m.defaultDriver
	connections := m.connections
	defaultName := m.defaultName
	databaseName := m.databaseName
	eventDispatcher := m.eventDispatcher
	rawEventDispatcher := m.rawEventDispatcher
	bus := m.txEventBus
	logger := m.logger
	m.mu.RUnlock()

	if defaultDriver == nil || tx == nil {
		return m
	}

	return &Manager{
		defaultDriver:      &txDriver{Driver: defaultDriver, tx: tx},
		connections:        connections,
		defaultName:        defaultName,
		databaseName:       databaseName,
		eventDispatcher:    eventDispatcher,
		rawEventDispatcher: rawEventDispatcher,
		txEventBus:         bus,
		logger:             logger,
	}
}

// Begin starts a new transaction.
func (m *Manager) Begin(ctx context.Context) (*sql.Tx, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return m.defaultDriver.BeginTx(ctx, nil)
}

// Shutdown closes the default database connection and all named connections,
// honoring the context deadline.
func (m *Manager) Shutdown(ctx context.Context) error {
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return errors.New("orm: no database connection")
	}
	return m.defaultDriver.Ping()
}

// DefaultDriver returns the default database driver (used internally by model Save).
func (m *Manager) DefaultDriver() drivers.Driver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.defaultDriver
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
// query and transaction lifecycle events. The supplied function is adapted
// into a typed dispatcher so orm.dispatchEvent can pass Event values without
// losing static type information.
func (m *Manager) SetEventDispatcher(fn func(event any) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		m.eventDispatcher = nil
		m.rawEventDispatcher = nil
		return
	}
	m.rawEventDispatcher = fn
	m.eventDispatcher = func(event Event) error {
		return fn(event)
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
func flushBufferedEntry(entry events.BufferedEvent, bus events.Dispatcher, raw func(any) error) error {
	if bus != nil {
		switch entry.Kind() {
		case events.KindDispatch:
			return bus.Dispatch(entry.Event())
		case events.KindDispatchNow:
			return bus.DispatchNow(entry.Event())
		case events.KindDispatchAsync:
			return bus.DispatchAsync(entry.Event())
		case events.KindDispatchAfter:
			return bus.DispatchAfter(entry.Event(), entry.Delay())
		case events.KindUntil:
			_, err := bus.Until(entry.Event())
			return err
		default:
			return bus.Dispatch(entry.Event())
		}
	}
	if raw != nil {
		return raw(entry.Event())
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

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *Manager) dispatchEvent(event Event) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		_ = fn(event)
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
