package drivers

import (
	"database/sql"
	"fmt"
)

// BaseDriver provides shared implementations for common driver operations.
// Embed this in concrete drivers to eliminate duplicated Close, Ping, DB,
// Query, QueryRow, Exec, Begin, BeginTx, CreateTable, and DropTable methods.
type BaseDriver struct {
	db     *sql.DB
	Config ConnectionConfig
}

// Close closes the database connection.
func (b *BaseDriver) Close() error {
	if b.db != nil {
		return b.db.Close()
	}
	return nil
}

// Ping verifies the connection to the database.
func (b *BaseDriver) Ping() error {
	if b.db == nil {
		return fmt.Errorf("no database connection")
	}
	return b.db.Ping()
}

// DB returns the underlying *sql.DB instance.
func (b *BaseDriver) DB() *sql.DB {
	return b.db
}

// Query executes a query that returns rows, with optional query logging.
func (b *BaseDriver) Query(query string, args ...any) (*sql.Rows, error) {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.Query(query, args...)
}

// QueryRow executes a query that returns at most one row, with optional query logging.
func (b *BaseDriver) QueryRow(query string, args ...any) *sql.Row {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.QueryRow(query, args...)
}

// Exec executes a query that doesn't return rows, with optional query logging.
func (b *BaseDriver) Exec(query string, args ...any) (sql.Result, error) {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.Exec(query, args...)
}

// Begin starts a transaction.
func (b *BaseDriver) Begin() (*sql.Tx, error) {
	return b.db.Begin()
}

// BeginTx starts a transaction with options.
func (b *BaseDriver) BeginTx() (*sql.Tx, error) {
	return b.db.Begin()
}

// ConfigurePool sets connection pool parameters on the given db.
func (b *BaseDriver) ConfigurePool(db *sql.DB) {
	if b.Config.MaxIdleConns > 0 {
		db.SetMaxIdleConns(b.Config.MaxIdleConns)
	}
	if b.Config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(b.Config.MaxOpenConns)
	}
	if b.Config.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(b.Config.ConnMaxLifetime)
	}
	if b.Config.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(b.Config.ConnMaxIdleTime)
	}
}

// openAndPing opens a database connection and verifies it with a ping.
func openAndPing(driverName, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}
	return db, nil
}

// CreateTableWith creates a new table using the provided grammar.
func (b *BaseDriver) CreateTableWith(grammar QueryGrammar, name string, definition func(*Table)) error {
	table := &Table{Name: name}
	definition(table)

	sql := grammar.CompileCreateTable(name, table)
	_, err := b.db.Exec(sql)
	return err
}

// DropTableWith drops a table using the provided grammar.
func (b *BaseDriver) DropTableWith(grammar QueryGrammar, name string) error {
	sql := grammar.CompileDropTable(name)
	_, err := b.db.Exec(sql)
	return err
}
