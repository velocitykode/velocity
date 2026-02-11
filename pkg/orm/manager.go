package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/pkg/orm/drivers"
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

// Manager manages database connections. It is the instance-based alternative
// to the package-level global functions.
type Manager struct {
	mu            sync.RWMutex
	defaultDriver drivers.Driver
	connections   map[string]drivers.Driver
	defaultName   string
	databaseName  string
}

// NewManager creates a new ORM Manager with a connected database driver.
func NewManager(config ManagerConfig) (*Manager, error) {
	factory, exists := driverFactories[config.Driver]
	if !exists {
		return nil, fmt.Errorf("orm: driver %s not registered", config.Driver)
	}

	driver := factory()

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

// Close closes the default database connection and all named connections.
func (m *Manager) Close() error {
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

func stringOrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}
