package drivers

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sync"
)

// osStderr is the stderr destination for deprecation warnings. It is a
// package-level variable so tests can redirect output without touching
// the global os.Stderr.
var osStderr io.Writer = os.Stderr

// BaseDriver provides shared implementations for common driver operations.
// Embed this in concrete drivers to eliminate duplicated Close, Ping, DB,
// Query, QueryRow, Exec, Begin, BeginTx, CreateTable, and DropTable methods.
type BaseDriver struct {
	db     *sql.DB
	Config ConnectionConfig
	// deprecatedShimWarned guards a one-time warning printed the first time
	// the legacy Query/QueryRow/Exec shims are invoked so callers are nudged
	// to migrate to the *Context equivalents without spamming logs.
	deprecatedShimWarned sync.Once
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

// warnDeprecatedShim emits a one-time stderr warning advising callers that
// the non-context-aware Query/QueryRow/Exec helpers are deprecated. The
// *Context variants should be preferred so cancellation and deadlines
// propagate end-to-end.
func (b *BaseDriver) warnDeprecatedShim() {
	b.deprecatedShimWarned.Do(func() {
		fmt.Fprintf(osStderr, "velocity/orm: deprecated — drivers.Query/QueryRow/Exec without context. use QueryContext/QueryRowContext/ExecContext.\n")
	})
}

// Query executes a query that returns rows.
//
// Deprecated: use QueryContext to propagate cancellation and deadlines.
// This shim forwards to QueryContext with context.TODO() and emits a
// one-time warning on first use.
func (b *BaseDriver) Query(query string, args ...any) (*sql.Rows, error) {
	b.warnDeprecatedShim()
	return b.QueryContext(context.TODO(), query, args...)
}

// QueryRow executes a query that returns at most one row.
//
// Deprecated: use QueryRowContext to propagate cancellation and deadlines.
// This shim forwards to QueryRowContext with context.TODO() and emits a
// one-time warning on first use.
func (b *BaseDriver) QueryRow(query string, args ...any) *sql.Row {
	b.warnDeprecatedShim()
	return b.QueryRowContext(context.TODO(), query, args...)
}

// Exec executes a query that doesn't return rows.
//
// Deprecated: use ExecContext to propagate cancellation and deadlines.
// This shim forwards to ExecContext with context.TODO() and emits a
// one-time warning on first use.
func (b *BaseDriver) Exec(query string, args ...any) (sql.Result, error) {
	b.warnDeprecatedShim()
	return b.ExecContext(context.TODO(), query, args...)
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

// Begin starts a transaction.
func (b *BaseDriver) Begin() (*sql.Tx, error) {
	return b.db.Begin()
}

// BeginTx starts a transaction with the given context and options. Pass
// opts = nil to use the underlying driver's defaults; that is equivalent
// to Begin but lets callers still flow a context through.
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

// openAndPing opens a database connection and verifies it with a ping.
func openAndPing(driverName, dsn string) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("velocity/orm: failed to open database: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("velocity/orm: failed to ping database: %w", err)
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
