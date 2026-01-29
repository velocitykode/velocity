package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/velocitykode/velocity/pkg/orm/drivers"
)

var (
	// Global driver instance
	defaultDriver drivers.Driver
	driverMu      sync.RWMutex

	// Driver registry
	driverFactories = make(map[string]func() drivers.Driver)

	// Connection manager for multiple databases
	connections = make(map[string]drivers.Driver)
	connMu      sync.RWMutex

	// Current database name
	currentDatabaseName string

	// Test transaction (for RefreshDatabase pattern)
	testTx   *sql.Tx
	testTxMu sync.RWMutex
)

// Init initializes the ORM with the specified driver and configuration
func Init(driverName string, config map[string]any) error {
	driverMu.Lock()
	defer driverMu.Unlock()

	factory, exists := driverFactories[driverName]
	if !exists {
		return fmt.Errorf("driver %s not registered", driverName)
	}

	driver := factory()

	// Convert config map to ConnectionConfig
	connConfig := drivers.ConnectionConfig{
		Driver:   driverName,
		Host:     getStringOrDefault(config, "host", "localhost"),
		Port:     getStringOrDefault(config, "port", getDefaultPort(driverName)),
		Database: getStringOrDefault(config, "database", ""),
		Username: getStringOrDefault(config, "username", ""),
		Password: getStringOrDefault(config, "password", ""),
		Charset:  getStringOrDefault(config, "charset", "utf8mb4"),
	}

	// Connection pool settings
	if v, ok := config["max_idle_conns"].(int); ok {
		connConfig.MaxIdleConns = v
	} else {
		connConfig.MaxIdleConns = 10
	}

	if v, ok := config["max_open_conns"].(int); ok {
		connConfig.MaxOpenConns = v
	} else {
		connConfig.MaxOpenConns = 100
	}

	if v, ok := config["conn_max_lifetime"].(int); ok {
		connConfig.ConnMaxLifetime = time.Duration(v) * time.Second
	} else {
		connConfig.ConnMaxLifetime = 3600 * time.Second
	}

	// Logging settings
	if v, ok := config["log_queries"].(bool); ok {
		connConfig.LogQueries = v
	}

	if v, ok := config["slow_query_threshold"].(string); ok {
		if duration, err := time.ParseDuration(v); err == nil {
			connConfig.SlowQueryThreshold = duration
		}
	}

	if err := driver.Connect(connConfig); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	defaultDriver = driver
	currentDatabaseName = connConfig.Database
	return nil
}

// RegisterDriver registers a new database driver
func RegisterDriver(name string, factory func() drivers.Driver) {
	driverFactories[name] = factory
}

// SetConnection sets a named database connection
func SetConnection(name string, driver drivers.Driver) {
	connMu.Lock()
	defer connMu.Unlock()
	connections[name] = driver
}

// Connection returns a named database connection
func Connection(name string) (drivers.Driver, error) {
	connMu.RLock()
	defer connMu.RUnlock()

	driver, exists := connections[name]
	if !exists {
		return nil, fmt.Errorf("connection %s not found", name)
	}
	return driver, nil
}

// DB returns the underlying *sql.DB instance
func DB() *sql.DB {
	driverMu.RLock()
	defer driverMu.RUnlock()

	if defaultDriver == nil {
		return nil
	}
	return defaultDriver.DB()
}

// Close closes the database connection
func Close() error {
	driverMu.Lock()
	defer driverMu.Unlock()

	if defaultDriver == nil {
		return nil
	}

	err := defaultDriver.Close()
	defaultDriver = nil
	return err
}

// Ping verifies the database connection
func Ping() error {
	driver := getCurrentDriver()
	if driver == nil {
		return errors.New("no database connection")
	}
	return driver.Ping()
}

// SetMaxIdleConns sets the maximum number of idle connections
func SetMaxIdleConns(n int) {
	if db := DB(); db != nil {
		db.SetMaxIdleConns(n)
	}
}

// SetMaxOpenConns sets the maximum number of open connections
func SetMaxOpenConns(n int) {
	if db := DB(); db != nil {
		db.SetMaxOpenConns(n)
	}
}

// SetConnMaxLifetime sets the maximum lifetime of connections
func SetConnMaxLifetime(d time.Duration) {
	if db := DB(); db != nil {
		db.SetConnMaxLifetime(d)
	}
}

// SetConnMaxIdleTime sets the maximum idle time of connections
func SetConnMaxIdleTime(d time.Duration) {
	if db := DB(); db != nil {
		db.SetConnMaxIdleTime(d)
	}
}

// Stats returns database connection pool statistics
func Stats() sql.DBStats {
	if db := DB(); db != nil {
		return db.Stats()
	}
	return sql.DBStats{}
}

// GetDriver returns the name of the current database driver
func GetDriver() string {
	driver := getCurrentDriver()
	if driver == nil {
		return ""
	}
	return driver.DriverName()
}

// GetDatabaseName returns the name of the current database
func GetDatabaseName() string {
	driverMu.RLock()
	defer driverMu.RUnlock()
	return currentDatabaseName
}

// Transaction executes a function within a database transaction
func Transaction(fn func() error) error {
	driver := getCurrentDriver()
	if driver == nil {
		return errors.New("no database connection")
	}

	tx, err := driver.Begin()
	if err != nil {
		return err
	}

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// Begin starts a new transaction
func Begin() (*sql.Tx, error) {
	driver := getCurrentDriver()
	if driver == nil {
		return nil, errors.New("no database connection")
	}
	return driver.Begin()
}

// SetTx sets a test transaction for RefreshDatabase pattern
// All queries will use this transaction until ClearTx is called
func SetTx(tx *sql.Tx) {
	testTxMu.Lock()
	defer testTxMu.Unlock()
	testTx = tx
}

// ClearTx clears the test transaction
func ClearTx() {
	testTxMu.Lock()
	defer testTxMu.Unlock()
	testTx = nil
}

// GetTx returns the current test transaction (nil if not in test)
func GetTx() *sql.Tx {
	testTxMu.RLock()
	defer testTxMu.RUnlock()
	return testTx
}

// Executor returns the current query executor (transaction if set, otherwise DB)
func Executor() QueryExecutor {
	testTxMu.RLock()
	defer testTxMu.RUnlock()
	if testTx != nil {
		return testTx
	}
	return DB()
}

// QueryExecutor interface for *sql.DB and *sql.Tx
type QueryExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Raw executes a raw SQL query
func Raw(query string, args ...any) *sql.Rows {
	driver := getCurrentDriver()
	if driver == nil {
		return nil
	}

	rows, err := driver.Query(query, args...)
	if err != nil {
		// Log error
		return nil
	}
	return rows
}

// Exec executes a raw SQL statement
func Exec(query string, args ...any) (sql.Result, error) {
	driver := getCurrentDriver()
	if driver == nil {
		return nil, errors.New("no database connection")
	}
	return driver.Exec(query, args...)
}

// Migrate runs database migrations
// This is a placeholder - actual implementation should use pkg/orm/migrate package
// Example: migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver()); migrator.Up()
func Migrate() error {
	return errors.New("use pkg/orm/migrate package for migration operations")
}

// Rollback rolls back migrations
// This is a placeholder - actual implementation should use pkg/orm/migrate package
// Example: migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver()); migrator.Down(steps)
func Rollback(steps int) error {
	return errors.New("use pkg/orm/migrate package for migration operations")
}

// Fresh drops all tables and re-runs migrations
func Fresh() error {
	return errors.New("use pkg/orm/migrate package for migration operations")
}

// Seed runs database seeders
func Seed(seeders ...string) error {
	// This would run database seeders
	// Implementation depends on seeder system
	return nil
}

// Internal helper functions

func getCurrentDriver() drivers.Driver {
	driverMu.RLock()
	defer driverMu.RUnlock()
	return defaultDriver
}

func getStringOrDefault(config map[string]any, key, defaultValue string) string {
	if v, ok := config[key].(string); ok && v != "" {
		return v
	}
	return defaultValue
}

func getDefaultPort(driver string) string {
	switch driver {
	case "mysql":
		return "3306"
	case "postgres":
		return "5432"
	case "sqlite":
		return ""
	default:
		return ""
	}
}

// Hash hashes a password (utility function for seeders)
func Hash(password string) string {
	// This would use bcrypt or similar
	// For now, return as-is
	return password
}
