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

	// OperatorRegistry returns dialect-specific operators (JSONB, FTS,
	// array overlap, ...) that the typed Where chain admits in addition
	// to the built-in scalar allowlist. Returns nil when the driver
	// declares no extension operators; built-in scalars work either way.
	OperatorRegistry() map[string]OperatorSpec
}

// ConnectionConfig holds database connection settings
type ConnectionConfig struct {
	Driver    string
	Host      string
	Port      string
	Database  string
	Username  string
	Password  string
	Charset   string
	Collation string
	Prefix    string
	Schema    string
	SSLMode   string // postgres: sslmode (disable/prefer/require/verify-ca/verify-full)
	TLS       string // mysql: tls= value (true/false/skip-verify/preferred/named-profile)
	// TimeZone sets the database SESSION timezone (postgres `TimeZone=`,
	// mysql `time_zone='...'`), which affects in-database functions and
	// timestamptz/TIMESTAMP rendering only. It never affects how bound
	// time.Time values are encoded: storage is unconditionally UTC (see
	// NormalizeTimeArgs). SQLite has no session timezone; unused there.
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

// ColumnSchema describes one column as read from a live database schema.
// It is distinct from model-derived column metadata, which is inferred from
// Go structs rather than introspected from the connected database. DataType
// and Default preserve the dialect's reported values rather than normalizing
// them, so callers may see values such as PostgreSQL "character varying",
// MySQL "varchar(255)", and SQLite's declared type.
type ColumnSchema struct {
	Name string
	// DataType is the raw dialect-reported column type, not a normalized
	// framework type.
	DataType string
	Nullable bool
	// Default is the raw dialect-reported default expression. It is nil when
	// the database reports no default.
	Default    *string
	PrimaryKey bool
}

// SchemaIntrospector is implemented by drivers that can inspect their live
// database schema through a dialect-agnostic API.
type SchemaIntrospector interface {
	ListTables(ctx context.Context) ([]string, error)
	DescribeTable(ctx context.Context, table string) ([]ColumnSchema, error)
}

// IntrospectionGrammar is implemented by grammars that compile schema
// introspection SQL. It is optional so third-party QueryGrammar
// implementations are not forced to add methods when they do not support
// introspection.
type IntrospectionGrammar interface {
	CompileListTables() string
	CompileDescribeTable(table string) string
}

// CreateIndexGrammar is implemented by grammars whose dialect declares indexes
// as separate CREATE INDEX statements rather than inline in CREATE TABLE.
// PostgreSQL and SQLite reject the MySQL-style inline "INDEX name (cols)" clause,
// so their grammars compile each Table.Index into its own statement here and
// CreateTableWith executes them after the table statement. It is optional: a
// grammar that does not implement it (e.g. MySQL, which emits inline INDEX
// clauses) keeps whatever index handling its CompileCreateTable performs.
type CreateIndexGrammar interface {
	// CompileCreateIndexes returns one CREATE [UNIQUE] INDEX statement per entry
	// in table.Indexes, targeting the table named name. It returns nil when
	// table.Indexes is empty.
	CompileCreateIndexes(name string, table *Table) []string
}

// ReturningGrammar is implemented by grammars whose dialect supports
// RETURNING on UPDATE / DELETE statements (currently PostgreSQL; SQLite
// 3.35+ and MariaDB 10.5+ are additional candidates).
//
// The ORM's bulk-hook surface uses this capability to capture affected
// primary keys atomically with the write, eliminating the pre-SELECT race
// window that drivers without RETURNING must accept.
//
// Implementing this interface is a contract: the grammar guarantees that
// the compiled statement returns one row per affected row containing the
// primary-key column, atomically with the write. Implementations whose
// dialect does NOT actually deliver atomic capture must NOT implement
// the interface, regardless of whether they happen to support a
// RETURNING-like syntax. The bulk-hook path treats the assertion as
// authoritative and skips the pre-SELECT fallback.
//
// Grammars that do not implement ReturningGrammar fall back to the
// pre-SELECT path.
type ReturningGrammar interface {
	CompileUpdateReturning(table string, values map[string]any, conditions []Condition, pkCol string) (string, []any)
	CompileDeleteReturning(table string, conditions []Condition, pkCol string) (string, []any)
}

// VectorGrammar is implemented by grammars whose dialect supports vector
// similarity search (e.g. PostgreSQL with the pgvector extension). It is an
// optional capability, mirroring ReturningGrammar: it is NOT part of the
// mandatory QueryGrammar contract, and callers detect it by type-asserting the
// value returned from Driver.Grammar(). Grammars whose dialect cannot evaluate
// vector distances must NOT implement it, so a vector query on such a driver
// fails with a clear "unsupported" error rather than degrading to broken SQL.
//
// Unlike ReturningGrammar (which has a valid non-RETURNING fallback), a vector
// query has no meaningful fallback on a non-vector dialect, so the query
// builder returns an error when the assertion fails instead of silently
// dropping the clause.
type VectorGrammar interface {
	// VectorDistanceExpr returns a SQL expression that computes the distance
	// between quotedColumn (an already-quoted identifier produced by
	// QuoteIdentifier) and a single bound vector parameter, represented by one
	// "?" placeholder. metric selects the distance function; an unsupported
	// metric returns an error. The bound parameter is supplied separately as a
	// driver.Valuer (orm.Vector) that renders the pgvector text literal, and the
	// returned expression casts the placeholder to the dialect's vector type.
	//
	// Example (postgres): VectorDistanceExpr(`"embedding"`, "cosine") returns
	// `"embedding" <=> ?::vector`.
	VectorDistanceExpr(quotedColumn, metric string) (string, error)
}

// SelectQuery represents a SELECT query structure
type SelectQuery struct {
	Table   string
	Columns []string
	// RawColumns are trusted raw SQL projections appended to the
	// SELECT list after Columns. Each RawColumn carries its own
	// bound arguments; grammars emit Expr verbatim and append Args
	// to the parameter list. Grammars whose dialect uses numbered
	// placeholders (e.g. PostgreSQL) rewrite any "?" inside Expr to
	// the appropriate placeholder at compile time.
	//
	// Populated by Query[T].SelectRaw; framework-internal aggregates
	// (Count, Sum, Avg, ...) also build their projection through
	// this field so the Columns whitelist can stay strict for
	// untrusted input.
	RawColumns    []RawColumn
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

	// Spec, when non-nil, names a driver-registered OperatorSpec. The
	// grammar emits Spec.Template instead of the built-in operator switch.
	// Nil for built-in scalar operators (=, <>, IN, BETWEEN, ...).
	Spec *OperatorSpec
}

// Order represents an ORDER BY clause.
//
// An Order is either a plain column sort (Column/Direction populated) or a
// raw-expression sort (Expr/Args populated). When Expr is non-empty it is
// emitted verbatim into the ORDER BY list and its bound Args are appended to
// the statement's parameter stream after the WHERE/HAVING args, mirroring how
// RawColumn projects a raw expression. Grammars that use numbered placeholders
// (PostgreSQL) rewrite any "?" inside Expr to the appropriate $N at compile
// time. Column is ignored when Expr is set; Direction still applies.
//
// SECURITY: Expr is emitted verbatim and is therefore a SQL-injection vector if
// built from user input. Only server-constructed, trusted SQL belongs in Expr;
// user-controlled values belong in Args as bound parameters. The vector
// similarity helpers (Query.OrderByDistance) build Expr from an allowlisted
// distance operator and a grammar-quoted identifier, never from raw input.
type Order struct {
	Column    string
	Direction string

	// Expr, when non-empty, is a trusted raw SQL ordering expression with "?"
	// placeholders, e.g. `"embedding" <=> ?::vector`. Takes precedence over
	// Column.
	Expr string
	// Args are the bound parameters referenced by "?" placeholders in Expr.
	Args []any
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
