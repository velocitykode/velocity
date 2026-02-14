package drivers

import (
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

	// Query execution
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
	Exec(query string, args ...any) (sql.Result, error)

	// Transaction support
	Begin() (*sql.Tx, error)
	BeginTx() (*sql.Tx, error)

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
	SSLMode            string
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

// Condition represents a WHERE condition
type Condition struct {
	Column   string
	Operator string
	Value    any
	Type     string // "and" or "or"
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
