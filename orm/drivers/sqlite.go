package drivers

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// hasDotDotTraversal checks if a cleaned path still contains ".." traversal components.
func hasDotDotTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// SQLiteDriver implements the Driver interface for SQLite
type SQLiteDriver struct {
	BaseDriver
}

// NewSQLiteDriver creates a new SQLite driver instance
func NewSQLiteDriver() Driver {
	return &SQLiteDriver{}
}

// Connect establishes a connection to SQLite database
func (d *SQLiteDriver) Connect(config ConnectionConfig) error {
	d.Config = config

	// Build DSN
	dsn := config.Database
	if dsn == "" {
		dsn = ":memory:"
	}

	// If dsn is just a filename (no path separators), put it in database/ folder
	if dsn != ":memory:" && !strings.Contains(dsn, "/") && !strings.Contains(dsn, "\\") {
		dsn = "database/" + dsn
	}

	// Validate path to prevent directory traversal attacks
	if dsn != ":memory:" {
		cleanPath := filepath.Clean(dsn)
		if hasDotDotTraversal(cleanPath) {
			return fmt.Errorf("database path contains invalid traversal component: %s", dsn)
		}
		dsn = cleanPath
	}

	// Create directory if it doesn't exist (for file-based databases)
	if dsn != ":memory:" && strings.Contains(dsn, "/") {
		dir := filepath.Dir(dsn)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
	}

	// Add connection parameters
	params := []string{}
	if config.TimeZone != "" {
		params = append(params, "_loc="+config.TimeZone)
	}

	if len(params) > 0 {
		dsn += "?" + strings.Join(params, "&")
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}

	// Configure connection pool
	db.SetMaxIdleConns(config.MaxIdleConns)
	db.SetMaxOpenConns(config.MaxOpenConns)
	db.SetConnMaxLifetime(config.ConnMaxLifetime)

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return err
	}

	// Enable WAL mode for better concurrency
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		// Ignore error for in-memory databases
		if config.Database != ":memory:" {
			return err
		}
	}

	d.db = db
	return nil
}

// HasTable checks if a table exists
func (d *SQLiteDriver) HasTable(name string) bool {
	sql := d.Grammar().CompileHasTable(name)
	var count int
	err := d.db.QueryRow(sql, name).Scan(&count)
	return err == nil && count > 0
}

// HasColumn checks if a column exists in a table
func (d *SQLiteDriver) HasColumn(table, column string) bool {
	sql := d.Grammar().CompileHasColumn(table, column)
	rows, err := d.db.Query(sql, table)
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notnull int
		var dfltValue *string
		var pk int

		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			continue
		}

		if name == column {
			return true
		}
	}
	return false
}

// CreateTable creates a new table
func (d *SQLiteDriver) CreateTable(name string, definition func(*Table)) error {
	return d.CreateTableWith(d.Grammar(), name, definition)
}

// DropTable drops a table
func (d *SQLiteDriver) DropTable(name string) error {
	return d.DropTableWith(d.Grammar(), name)
}

// Grammar returns the SQLite query grammar
func (d *SQLiteDriver) Grammar() QueryGrammar {
	return &SQLiteGrammar{}
}

// DriverName returns the driver name
func (d *SQLiteDriver) DriverName() string {
	return "sqlite"
}

// SQLiteGrammar implements QueryGrammar for SQLite
type SQLiteGrammar struct{}

// CompileSelect compiles a SELECT query
func (g *SQLiteGrammar) CompileSelect(query *SelectQuery) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("SELECT ")

	if query.Distinct {
		sql.WriteString("DISTINCT ")
	}

	// Columns
	if len(query.Columns) > 0 {
		for i, col := range query.Columns {
			if i > 0 {
				sql.WriteString(", ")
			}
			// Don't quote expressions like COUNT(*) or wildcard *
			if strings.Contains(col, "(") || col == "*" {
				sql.WriteString(col)
			} else {
				sql.WriteString(g.QuoteIdentifier(col))
			}
		}
	} else {
		sql.WriteString("*")
	}

	// FROM
	sql.WriteString(" FROM ")
	sql.WriteString(g.QuoteIdentifier(query.Table))

	// JOINs
	for _, join := range query.Joins {
		sql.WriteString(" ")
		sql.WriteString(join.Type)
		sql.WriteString(" JOIN ")
		sql.WriteString(g.QuoteIdentifier(join.Table))
		sql.WriteString(" ON ")
		sql.WriteString(join.On)
	}

	// WHERE
	if len(query.Conditions) > 0 {
		sql.WriteString(" WHERE ")
		for i, cond := range query.Conditions {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)

			if cond.Operator != "IS NULL" && cond.Operator != "IS NOT NULL" {
				sql.WriteString(" ?")
				args = append(args, cond.Value)
			}
		}
	}

	// GROUP BY
	if len(query.Groups) > 0 {
		sql.WriteString(" GROUP BY ")
		for i, group := range query.Groups {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(g.QuoteIdentifier(group))
		}
	}

	// HAVING
	if len(query.Having) > 0 {
		sql.WriteString(" HAVING ")
		for i, cond := range query.Having {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)
			sql.WriteString(" ?")
			args = append(args, cond.Value)
		}
	}

	// ORDER BY
	if len(query.Orders) > 0 {
		sql.WriteString(" ORDER BY ")
		for i, order := range query.Orders {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(g.QuoteIdentifier(order.Column))
			sql.WriteString(" ")
			sql.WriteString(order.Direction)
		}
	}

	// LIMIT
	if query.Limit != nil {
		sql.WriteString(" LIMIT ")
		sql.WriteString(fmt.Sprintf("%d", *query.Limit))
	}

	// OFFSET
	if query.Offset != nil {
		sql.WriteString(" OFFSET ")
		sql.WriteString(fmt.Sprintf("%d", *query.Offset))
	}

	// SQLite doesn't support FOR UPDATE SKIP LOCKED
	// We'll handle locking at application level for SQLite
	// Just ignore the flags here

	return sql.String(), args
}

// CompileInsert compiles an INSERT query
func (g *SQLiteGrammar) CompileInsert(table string, columns []string, values [][]any) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("INSERT INTO ")
	sql.WriteString(g.QuoteIdentifier(table))

	if len(columns) > 0 {
		sql.WriteString(" (")
		for i, col := range columns {
			if i > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(g.QuoteIdentifier(col))
		}
		sql.WriteString(")")
	}

	sql.WriteString(" VALUES ")

	for i, row := range values {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString("(")
		for j := range row {
			if j > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString("?")
			args = append(args, row[j])
		}
		sql.WriteString(")")
	}

	return sql.String(), args
}

// CompileUpdate compiles an UPDATE query
func (g *SQLiteGrammar) CompileUpdate(table string, values map[string]any, conditions []Condition) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("UPDATE ")
	sql.WriteString(g.QuoteIdentifier(table))
	sql.WriteString(" SET ")

	i := 0
	for column, value := range values {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString(g.QuoteIdentifier(column))

		// Raw SQL values (e.g. CURRENT_TIMESTAMP) emit verbatim; all
		// other values bind as parameters.
		if rawVal, ok := value.(RawSQL); ok {
			sql.WriteString(" = ")
			sql.WriteString(string(rawVal))
		} else {
			sql.WriteString(" = ?")
			args = append(args, value)
		}
		i++
	}

	// WHERE
	if len(conditions) > 0 {
		sql.WriteString(" WHERE ")
		for i, cond := range conditions {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)
			sql.WriteString(" ?")
			args = append(args, cond.Value)
		}
	}

	return sql.String(), args
}

// CompileDelete compiles a DELETE query
func (g *SQLiteGrammar) CompileDelete(table string, conditions []Condition) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("DELETE FROM ")
	sql.WriteString(g.QuoteIdentifier(table))

	// WHERE
	if len(conditions) > 0 {
		sql.WriteString(" WHERE ")
		for i, cond := range conditions {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)
			sql.WriteString(" ?")
			args = append(args, cond.Value)
		}
	}

	return sql.String(), args
}

// CompileCreateTable compiles a CREATE TABLE query
func (g *SQLiteGrammar) CompileCreateTable(name string, table *Table) string {
	var sql strings.Builder

	sql.WriteString("CREATE TABLE ")
	sql.WriteString(g.QuoteIdentifier(name))
	sql.WriteString(" (")

	for i, column := range table.Columns {
		if i > 0 {
			sql.WriteString(", ")
		}

		sql.WriteString(g.QuoteIdentifier(column.Name))
		sql.WriteString(" ")
		sql.WriteString(g.getSQLiteType(column.Type))

		if column.Primary {
			sql.WriteString(" PRIMARY KEY")
		}
		if column.AutoIncrement {
			sql.WriteString(" AUTOINCREMENT")
		}
		if !column.Nullable {
			sql.WriteString(" NOT NULL")
		}
		if column.Unique {
			sql.WriteString(" UNIQUE")
		}
		if column.Default != nil {
			sql.WriteString(" DEFAULT ")
			sql.WriteString(fmt.Sprintf("%v", column.Default))
		}
	}

	sql.WriteString(")")

	return sql.String()
}

// CompileDropTable compiles a DROP TABLE query
func (g *SQLiteGrammar) CompileDropTable(name string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s", g.QuoteIdentifier(name))
}

// CompileHasTable compiles a query to check if table exists
func (g *SQLiteGrammar) CompileHasTable(name string) string {
	return "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?"
}

// CompileHasColumn compiles a query to check if column exists
func (g *SQLiteGrammar) CompileHasColumn(table, column string) string {
	return fmt.Sprintf("PRAGMA table_info(%s)", g.QuoteIdentifier(table))
}

// QuoteIdentifier quotes a database identifier
func (g *SQLiteGrammar) QuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// QuoteString quotes a string value
func (g *SQLiteGrammar) QuoteString(value string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "''"))
}

// Placeholder returns the placeholder for prepared statements
func (g *SQLiteGrammar) Placeholder(index int) string {
	return "?"
}

// getSQLiteType converts generic types to SQLite types
func (g *SQLiteGrammar) getSQLiteType(typ string) string {
	switch strings.ToUpper(typ) {
	case "BIGINT", "INT", "INTEGER", "SMALLINT", "TINYINT":
		return "INTEGER"
	case "DECIMAL", "NUMERIC", "REAL", "DOUBLE", "FLOAT":
		return "REAL"
	case "VARCHAR", "CHAR", "TEXT", "CLOB":
		return "TEXT"
	case "BLOB", "BINARY", "VARBINARY":
		return "BLOB"
	case "BOOLEAN", "BOOL":
		return "INTEGER"
	case "DATE", "DATETIME", "TIMESTAMP":
		return "TEXT"
	case "JSON", "JSONB":
		return "TEXT"
	default:
		return typ
	}
}
