package drivers

import (
	"context"
	"database/sql"
	"fmt"
)

// BaseDriver provides shared implementations for common driver operations.
// Embed this in concrete drivers to eliminate duplicated Close, Ping, DB,
// Query, BeginTx, CreateTable, and DropTable methods.
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
		return fmt.Errorf("velocity/orm: no database connection")
	}
	return b.db.Ping()
}

// DB returns the underlying *sql.DB instance.
func (b *BaseDriver) DB() *sql.DB {
	return b.db
}

// QueryContext executes a query that returns rows, honoring the context
// for cancellation and deadlines.
func (b *BaseDriver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query that returns at most one row, honoring
// the context for cancellation and deadlines.
func (b *BaseDriver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.QueryRowContext(ctx, query, args...)
}

// ExecContext executes a query that doesn't return rows, honoring the
// context for cancellation and deadlines.
func (b *BaseDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if b.Config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, len(args))
	}
	return b.db.ExecContext(ctx, query, args...)
}

// BeginTx starts a transaction with the given context and options. Pass
// opts = nil to use the underlying driver's defaults.
func (b *BaseDriver) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return b.db.BeginTx(ctx, opts)
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

// OpenAndPing opens a database connection via the named database/sql driver,
// verifies it with a ping, applies the pool configuration from b.Config, and
// installs the handle on the receiver. It is the shared connect helper the
// per-driver leaf packages (orm/postgres, orm/mysql) reuse so the dial+ping
// logic and the unexported db field live in one place; b.Config must be set
// before calling.
//
// On any failure the handle is left nil and the error is returned wrapped;
// leaves add dialect-specific context (e.g. a redacted DSN) on top.
func (b *BaseDriver) OpenAndPing(driverName, dsn string) error {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return fmt.Errorf("velocity/orm: failed to open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return fmt.Errorf("velocity/orm: failed to ping database: %w", err)
	}
	b.ConfigurePool(db)
	b.db = db
	return nil
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
