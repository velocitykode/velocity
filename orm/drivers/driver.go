package drivers

import (
	"context"
	"database/sql"
	"time"
)

// Driver defines the interface for all database drivers
type Driver interface {
	// Connection management
	Connect(config ConnectionConfig) error
	Close() error
	Ping() error
	DB() *sql.DB

	// Context-aware query execution.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Transaction support. BeginTx takes a context and explicit options
	// (isolation level, read-only) so callers can stream those through the
	// driver interface instead of reaching around it via sql.DB.BeginTx.
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)

	// Schema operations
	CreateTable(name string, definition func(*Table)) error
	DropTable(name string) error
	HasTable(name string) bool
	HasColumn(table, column string) bool

	// Driver-specific
	Grammar() QueryGrammar
	DriverName() string
}

// ConnectionConfig holds database connection settings
type ConnectionConfig struct {
	Driver             string
	Host               string
	Port               string
	Database           string
	Username           string
	Password           string
	Charset            string
	Collation          string
	Prefix             string
	Schema             string
	SSLMode            string // postgres: sslmode (disable/prefer/require/verify-ca/verify-full)
	TLS                string // mysql: tls= value (true/false/skip-verify/preferred/named-profile)
	TimeZone           string
	MaxIdleConns       int
	MaxOpenConns       int
	ConnMaxLifetime    time.Duration
	ConnMaxIdleTime    time.Duration
	LogQueries         bool
	SlowQueryThreshold time.Duration
}

// QueryGrammar defines SQL dialect-specific query building
type QueryGrammar interface {
	// SELECT operations
	CompileSelect(query *SelectQuery) (string, []any)
	CompileInsert(table string, columns []string, values [][]any) (string, []any)
	CompileUpdate(table string, values map[string]any, conditions []Condition) (string, []any)
	CompileDelete(table string, conditions []Condition) (string, []any)

	// Schema operations
	CompileCreateTable(name string, table *Table) string
	CompileDropTable(name string) string
	CompileHasTable(name string) string
	CompileHasColumn(table, column string) string

	// Utilities
	QuoteIdentifier(name string) string
	QuoteString(value string) string
	Placeholder(index int) string
}

// ReturningGrammar is implemented by grammars whose dialect supports
// RETURNING on UPDATE / DELETE statements (currently PostgreSQL).
//
// The ORM's bulk-hook surface uses this capability to capture affected
// primary keys atomically with the write, eliminating the pre-SELECT race
// window that drivers without RETURNING (MySQL, SQLite) must accept.
//
// Grammars that do not implement ReturningGrammar fall back to the
// pre-SELECT path. Adding a new RETURNING-capable dialect (e.g. SQLite
// 3.35+, MariaDB 10.5+) is a matter of implementing this interface on
// the corresponding grammar.
type ReturningGrammar interface {
	CompileUpdateReturning(table string, values map[string]any, conditions []Condition, pkCol string) (string, []any)
	CompileDeleteReturning(table string, conditions []Condition, pkCol string) (string, []any)
}

// SelectQuery represents a SELECT query structure
type SelectQuery struct {
	Table         string
	Columns       []string
	Conditions    []Condition
	Orders        []Order
	Groups        []string
	Having        []Condition
	Limit         *int
	Offset        *int
	Joins         []Join
	Distinct      bool
	LockForUpdate bool
	SkipLocked    bool
}

// Condition represents a WHERE condition.
//
// A Condition is either a leaf predicate (Column/Operator/Value populated)
// or a parenthesized sub-group of conditions (Group populated). Type is
// "and" or "or" and applies to the relationship between this condition and
// the previous one in the same list. When Group is non-empty, Column,
// Operator, and Value are ignored and grammars must emit "(<sub>)" with
// the sub-conditions joined by their own Type fields. The first condition
// inside a Group always emits without a leading conjunction; the Type on
// later items inside the Group decides AND vs OR within the parens.
type Condition struct {
	Column   string
	Operator string
	Value    any
	Type     string // "and" or "or"

	// Group, when non-empty, marks this Condition as a parenthesized
	// sub-group. Grammars emit "(<grouped>)" instead of a leaf predicate.
	// Nesting is supported recursively.
	Group []Condition
}

// Order represents an ORDER BY clause
type Order struct {
	Column    string
	Direction string
}

// Join represents a JOIN clause
type Join struct {
	Type      string // INNER, LEFT, RIGHT, FULL
	Table     string
	On        string
	Condition []Condition
}

// Table represents a database table structure
type Table struct {
	Name    string
	Columns []Column
	Indexes []Index
	Primary string
}

// Column represents a database column
type Column struct {
	Name          string
	Type          string
	Size          int
	Nullable      bool
	Default       any
	AutoIncrement bool
	Primary       bool
	Unique        bool
	Comment       string
}

// Index represents a database index
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Type    string // BTREE, HASH, etc.
}
