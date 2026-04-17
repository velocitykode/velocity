package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

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
	SSLMode         string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
	LogQueries      bool
	SlowThreshold   time.Duration
}

// DefaultManagerConfig returns a ManagerConfig with sensible defaults.
// Driver, Database, Username, and Password must still be set by the caller.
func DefaultManagerConfig() ManagerConfig {
	return ManagerConfig{
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: 3600 * time.Second,
	}
}

// Validate checks that the ManagerConfig has the required fields set.
func (c ManagerConfig) Validate() error {
	if c.Driver == "" {
		return fmt.Errorf("orm: driver is required (sqlite, postgres, mysql)")
	}
	switch c.Driver {
	case "sqlite", "sqlite3", "postgres", "mysql":
	default:
		return fmt.Errorf("orm: unsupported driver %q", c.Driver)
	}
	return nil
}

// Database is the interface satisfied by *Manager. It covers the methods used
// through app.Services and router.Context for query execution, transactions,
// connection management, and event wiring.
type Database interface {
	DB() *sql.DB
	Raw(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
	Transaction(fn func(tx *sql.Tx) error) error
	Begin() (*sql.Tx, error)
	Shutdown(ctx context.Context) error
	Close() error // Deprecated: use Shutdown(ctx) instead.
	Ping() error
	DriverName() string
	DatabaseName() string
	Stats() sql.DBStats
	DefaultDriver() drivers.Driver
	Connection(name string) (drivers.Driver, error)
	AddConnection(name string, driver drivers.Driver)
	SetEventDispatcher(fn func(event interface{}) error)
}

// Verify *Manager implements Database at compile time.
var _ Database = (*Manager)(nil)

// Manager manages database connections. It is the instance-based alternative
// to the package-level global functions.
type Manager struct {
	mu              sync.RWMutex
	defaultDriver   drivers.Driver
	connections     map[string]drivers.Driver
	defaultName     string
	databaseName    string
	eventDispatcher func(event interface{}) error
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
func (m *Manager) Raw(query string, args ...any) (*sql.Rows, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	rows, err := m.defaultDriver.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("orm: raw query failed: %w", err)
	}
	return rows, nil
}

// Exec executes a raw SQL statement.
//
// WARNING: The caller is responsible for preventing SQL injection by using
// parameterized queries. Never concatenate user input into the query string.
func (m *Manager) Exec(query string, args ...any) (sql.Result, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return m.defaultDriver.Exec(query, args...)
}

// Transaction executes a function within a database transaction.
func (m *Manager) Transaction(fn func(tx *sql.Tx) error) error {
	m.mu.RLock()
	driver := m.defaultDriver
	m.mu.RUnlock()

	if driver == nil {
		return errors.New("orm: no database connection")
	}

	tx, err := driver.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				fmt.Fprintf(os.Stderr, "orm: rollback failed after panic: %v\n", rbErr)
			}
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			fmt.Fprintf(os.Stderr, "orm: rollback failed: %v (original error: %v)\n", rbErr, err)
		}
		return err
	}

	return tx.Commit()
}

// Begin starts a new transaction.
func (m *Manager) Begin() (*sql.Tx, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return m.defaultDriver.Begin()
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

// Close closes the default database connection and all named connections.
// Deprecated: use Shutdown(ctx) instead.
func (m *Manager) Close() error {
	return m.Shutdown(context.Background())
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

// SetEventDispatcher sets the function used to dispatch ORM events.
func (m *Manager) SetEventDispatcher(fn func(event interface{}) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (m *Manager) dispatchEvent(event interface{}) {
	m.mu.RLock()
	fn := m.eventDispatcher
	m.mu.RUnlock()
	if fn != nil {
		fn(event)
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
