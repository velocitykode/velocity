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
	// queryLogger receives every executed statement when Config.LogQueries
	// is set. It defaults to defaultQueryLogger (stdout via fmt.Printf) so
	// behavior is unchanged out of the box; SetQueryLogger swaps in a sink
	// that routes through the framework logger. It stays a plain func so the
	// drivers package never imports the log package (drivers is imported by
	// orm core; a log import would risk a cycle).
	queryLogger func(query string, argCount int)
}

// defaultQueryLogger writes the executed statement to stdout, preserving the
// historical fmt.Printf format used when LogQueries is enabled.
func defaultQueryLogger(query string, argCount int) {
	fmt.Printf("SQL: %s\nArgs: [%d params]\n", query, argCount)
}

// SetQueryLogger installs a sink for executed-statement logging, replacing the
// default stdout writer. Pass nil to restore the default. The sink is only
// invoked when Config.LogQueries is true.
func (b *BaseDriver) SetQueryLogger(fn func(query string, argCount int)) {
	b.queryLogger = fn
}

// logQuery emits one executed statement through the configured query logger
// when query logging is enabled.
func (b *BaseDriver) logQuery(query string, argCount int) {
	if !b.Config.LogQueries {
		return
	}
	logger := b.queryLogger
	if logger == nil {
		logger = defaultQueryLogger
	}
	logger(query, argCount)
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
	b.logQuery(query, len(args))
	return b.db.QueryContext(ctx, query, NormalizeTimeArgs(args)...)
}

// QueryRowContext executes a query that returns at most one row, honoring
// the context for cancellation and deadlines.
func (b *BaseDriver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	b.logQuery(query, len(args))
	return b.db.QueryRowContext(ctx, query, NormalizeTimeArgs(args)...)
}

// ExecContext executes a query that doesn't return rows, honoring the
// context for cancellation and deadlines.
func (b *BaseDriver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	b.logQuery(query, len(args))
	return b.db.ExecContext(ctx, query, NormalizeTimeArgs(args)...)
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
	if _, err := b.db.Exec(sql); err != nil {
		return err
	}

	// Grammars whose dialect declares indexes as separate CREATE INDEX
	// statements (PostgreSQL, SQLite) emit them here; MySQL folds indexes
	// inline into CompileCreateTable and does not implement this interface.
	if ig, ok := grammar.(CreateIndexGrammar); ok {
		for _, stmt := range ig.CompileCreateIndexes(name, table) {
			if _, err := b.db.Exec(stmt); err != nil {
				return err
			}
		}
	}
	return nil
}

// DropTableWith drops a table using the provided grammar.
func (b *BaseDriver) DropTableWith(grammar QueryGrammar, name string) error {
	sql := grammar.CompileDropTable(name)
	_, err := b.db.Exec(sql)
	return err
}
