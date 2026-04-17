package drivers

import (
	"fmt"
	"os"
	"strings"

	_ "github.com/lib/pq"
)

// escapePgDSNValue escapes a value for use in a PostgreSQL key=value DSN string.
//
// lib/pq / libpq key=value DSNs quote values containing whitespace by wrapping
// them in single quotes. Inside a single-quoted value, embedded single quotes
// and backslashes must be escaped with a backslash. All other characters are
// left intact. Empty values are emitted as empty single-quoted strings.
//
// References: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
func escapePgDSNValue(val string) string {
	if val == "" {
		return "''"
	}
	// Quoting is required when the value contains whitespace, '\'', backslash,
	// or the '=' used as the key/value separator.
	needsQuoting := false
	for _, r := range val {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\'' || r == '\\' || r == '=' {
			needsQuoting = true
			break
		}
	}
	if !needsQuoting {
		return val
	}
	// Inside single-quoted values, escape backslash first, then single quote.
	escaped := strings.ReplaceAll(val, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	return "'" + escaped + "'"
}

// redactDSNPassword replaces the password= value in a libpq key=value DSN
// string with "[REDACTED]" so DSN contents can be safely included in error
// messages and logs.
func redactDSNPassword(dsn string) string {
	// The DSN is a space-separated list of key=value pairs. The password
	// value may or may not be single-quoted depending on its contents.
	idx := strings.Index(dsn, "password=")
	if idx == -1 {
		return dsn
	}
	start := idx + len("password=")
	if start >= len(dsn) {
		return dsn[:start] + "[REDACTED]"
	}
	var end int
	if dsn[start] == '\'' {
		// Quoted value: scan past escape sequences until the closing quote.
		end = start + 1
		for end < len(dsn) {
			if dsn[end] == '\\' && end+1 < len(dsn) {
				end += 2
				continue
			}
			if dsn[end] == '\'' {
				end++
				break
			}
			end++
		}
	} else {
		// Unquoted value: read until the next whitespace.
		end = start
		for end < len(dsn) && dsn[end] != ' ' && dsn[end] != '\t' {
			end++
		}
	}
	return dsn[:idx] + "password=[REDACTED]" + dsn[end:]
}

// PostgresDriver implements the Driver interface for PostgreSQL
type PostgresDriver struct {
	BaseDriver
}

// NewPostgresDriver creates a new PostgreSQL driver instance
func NewPostgresDriver() Driver {
	return &PostgresDriver{}
}

// resolveSSLMode returns the effective sslmode for a Postgres connection.
//
// Precedence:
//  1. Config.SSLMode, if explicitly set by the caller.
//  2. DB_SSL_MODE env var, if set. Allows deployment-level opt-out.
//  3. "require" — the secure default. Applications must explicitly opt out
//     if they need to connect to an unencrypted server.
//
// Callers that need to disable TLS for local development can set
// DB_SSL_MODE=disable or Config.SSLMode="disable".
func resolveSSLMode(configured string) string {
	if configured != "" {
		return configured
	}
	if env := os.Getenv("DB_SSL_MODE"); env != "" {
		return env
	}
	return "require"
}

// Connect establishes a connection to PostgreSQL database
func (d *PostgresDriver) Connect(config ConnectionConfig) error {
	d.Config = config

	// Build DSN (PostgreSQL connection string)
	// Escape values to handle special characters (spaces, quotes, backslashes)
	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s",
		escapePgDSNValue(config.Host),
		escapePgDSNValue(config.Port),
		escapePgDSNValue(config.Username),
		escapePgDSNValue(config.Database),
	)

	// Add password only if provided
	if config.Password != "" {
		dsn += " password=" + escapePgDSNValue(config.Password)
	}

	// sslmode defaults to require — secure by default.
	dsn += " sslmode=" + escapePgDSNValue(resolveSSLMode(config.SSLMode))

	if config.TimeZone != "" {
		dsn += " TimeZone=" + escapePgDSNValue(config.TimeZone)
	}

	if config.Schema != "" {
		dsn += " search_path=" + escapePgDSNValue(config.Schema)
	}

	db, err := openAndPing("postgres", dsn)
	if err != nil {
		return fmt.Errorf("velocity/orm: postgres connect failed (dsn=%q): %w", redactDSNPassword(dsn), err)
	}

	d.ConfigurePool(db)
	d.db = db
	return nil
}

// HasTable checks if a table exists
func (d *PostgresDriver) HasTable(name string) bool {
	sql := d.Grammar().CompileHasTable(name)
	var exists bool
	err := d.db.QueryRow(sql, name).Scan(&exists)
	return err == nil && exists
}

// HasColumn checks if a column exists in a table
func (d *PostgresDriver) HasColumn(table, column string) bool {
	sql := d.Grammar().CompileHasColumn(table, column)
	var count int
	err := d.db.QueryRow(sql, table, column).Scan(&count)
	return err == nil && count > 0
}

// CreateTable creates a new table
func (d *PostgresDriver) CreateTable(name string, definition func(*Table)) error {
	return d.CreateTableWith(d.Grammar(), name, definition)
}

// DropTable drops a table
func (d *PostgresDriver) DropTable(name string) error {
	return d.DropTableWith(d.Grammar(), name)
}

// Grammar returns the PostgreSQL query grammar
func (d *PostgresDriver) Grammar() QueryGrammar {
	return &PostgresGrammar{}
}

// DriverName returns the driver name
func (d *PostgresDriver) DriverName() string {
	return "postgres"
}

// PostgresGrammar implements QueryGrammar for PostgreSQL
type PostgresGrammar struct{}

// CompileSelect compiles a SELECT query for PostgreSQL
func (g *PostgresGrammar) CompileSelect(query *SelectQuery) (string, []any) {
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
			// Handle special columns like COUNT(*)
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
		argIndex := 1
		for i, cond := range query.Conditions {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)

			switch cond.Operator {
			case "IS NULL", "IS NOT NULL":
				// No placeholder needed
			case "IN", "NOT IN":
				if values, ok := cond.Value.([]any); ok {
					sql.WriteString(" (")
					for j := range values {
						if j > 0 {
							sql.WriteString(", ")
						}
						sql.WriteString(fmt.Sprintf("$%d", argIndex))
						argIndex++
						args = append(args, values[j])
					}
					sql.WriteString(")")
				}
			case "BETWEEN":
				if values, ok := cond.Value.([]any); ok && len(values) == 2 {
					sql.WriteString(fmt.Sprintf(" $%d AND $%d", argIndex, argIndex+1))
					args = append(args, values[0], values[1])
					argIndex += 2
				}
			default:
				sql.WriteString(fmt.Sprintf(" $%d", argIndex))
				args = append(args, cond.Value)
				argIndex++
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
		argIndex := len(args) + 1
		for i, cond := range query.Having {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)
			sql.WriteString(fmt.Sprintf(" $%d", argIndex))
			args = append(args, cond.Value)
			argIndex++
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
		sql.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// OFFSET
	if query.Offset != nil {
		sql.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	// FOR UPDATE / SKIP LOCKED
	if query.LockForUpdate {
		sql.WriteString(" FOR UPDATE")
		if query.SkipLocked {
			sql.WriteString(" SKIP LOCKED")
		}
	}

	return sql.String(), args
}

// CompileInsert compiles an INSERT query for PostgreSQL
func (g *PostgresGrammar) CompileInsert(table string, columns []string, values [][]any) (string, []any) {
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

	argIndex := 1
	for i, row := range values {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString("(")
		for j := range row {
			if j > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(fmt.Sprintf("$%d", argIndex))
			args = append(args, row[j])
			argIndex++
		}
		sql.WriteString(")")
	}

	sql.WriteString(" RETURNING id")

	return sql.String(), args
}

// CompileUpdate compiles an UPDATE query for PostgreSQL
func (g *PostgresGrammar) CompileUpdate(table string, values map[string]any, conditions []Condition) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("UPDATE ")
	sql.WriteString(g.QuoteIdentifier(table))
	sql.WriteString(" SET ")

	i := 0
	argIndex := 1
	for column, value := range values {
		if i > 0 {
			sql.WriteString(", ")
		}
		sql.WriteString(g.QuoteIdentifier(column))

		// Handle special values
		if strVal, ok := value.(string); ok && strVal == "NOW()" {
			sql.WriteString(" = NOW()")
		} else {
			sql.WriteString(fmt.Sprintf(" = $%d", argIndex))
			args = append(args, value)
			argIndex++
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
			sql.WriteString(fmt.Sprintf(" $%d", argIndex))
			args = append(args, cond.Value)
			argIndex++
		}
	}

	return sql.String(), args
}

// CompileDelete compiles a DELETE query for PostgreSQL
func (g *PostgresGrammar) CompileDelete(table string, conditions []Condition) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("DELETE FROM ")
	sql.WriteString(g.QuoteIdentifier(table))

	// WHERE
	if len(conditions) > 0 {
		sql.WriteString(" WHERE ")
		argIndex := 1
		for i, cond := range conditions {
			if i > 0 {
				sql.WriteString(" ")
				sql.WriteString(strings.ToUpper(cond.Type))
				sql.WriteString(" ")
			}

			sql.WriteString(g.QuoteIdentifier(cond.Column))
			sql.WriteString(" ")
			sql.WriteString(cond.Operator)
			sql.WriteString(fmt.Sprintf(" $%d", argIndex))
			args = append(args, cond.Value)
			argIndex++
		}
	}

	return sql.String(), args
}

// CompileCreateTable compiles a CREATE TABLE query for PostgreSQL
func (g *PostgresGrammar) CompileCreateTable(name string, table *Table) string {
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
		sql.WriteString(g.getPostgresType(column))

		if column.Primary {
			sql.WriteString(" PRIMARY KEY")
		}
		// PostgreSQL uses SERIAL for auto-increment (type already set)
		if !column.Nullable {
			sql.WriteString(" NOT NULL")
		}
		if column.Unique {
			sql.WriteString(" UNIQUE")
		}
		if column.Default != nil {
			sql.WriteString(" DEFAULT ")
			switch v := column.Default.(type) {
			case string:
				sql.WriteString(fmt.Sprintf("'%s'", v))
			case bool:
				if v {
					sql.WriteString("TRUE")
				} else {
					sql.WriteString("FALSE")
				}
			default:
				sql.WriteString(fmt.Sprintf("%v", v))
			}
		}
	}

	// Add indexes
	for _, index := range table.Indexes {
		sql.WriteString(", ")
		if index.Unique {
			sql.WriteString("UNIQUE ")
		}
		sql.WriteString("INDEX ")
		sql.WriteString(g.QuoteIdentifier(index.Name))
		sql.WriteString(" (")
		for j, col := range index.Columns {
			if j > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(g.QuoteIdentifier(col))
		}
		sql.WriteString(")")
	}

	sql.WriteString(")")

	return sql.String()
}

// CompileDropTable compiles a DROP TABLE query for PostgreSQL
func (g *PostgresGrammar) CompileDropTable(name string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", g.QuoteIdentifier(name))
}

// CompileHasTable compiles a query to check if table exists in PostgreSQL
func (g *PostgresGrammar) CompileHasTable(name string) string {
	return `SELECT EXISTS (
		SELECT FROM information_schema.tables
		WHERE table_schema = 'public'
		AND table_name = $1
	)`
}

// CompileHasColumn compiles a query to check if column exists in PostgreSQL
func (g *PostgresGrammar) CompileHasColumn(table, column string) string {
	return `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = 'public'
		AND table_name = $1
		AND column_name = $2`
}

// QuoteIdentifier quotes a database identifier for PostgreSQL
func (g *PostgresGrammar) QuoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// QuoteString quotes a string value for PostgreSQL
func (g *PostgresGrammar) QuoteString(value string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "''"))
}

// Placeholder returns the placeholder for prepared statements in PostgreSQL
func (g *PostgresGrammar) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

// getPostgresType converts generic column types to PostgreSQL types
func (g *PostgresGrammar) getPostgresType(column Column) string {
	if column.AutoIncrement {
		// Use SERIAL for auto-increment integer columns
		switch strings.ToUpper(column.Type) {
		case "BIGINT":
			return "BIGSERIAL"
		case "SMALLINT":
			return "SMALLSERIAL"
		default:
			return "SERIAL"
		}
	}

	switch strings.ToUpper(column.Type) {
	case "INT", "INTEGER":
		return "INTEGER"
	case "BIGINT":
		return "BIGINT"
	case "SMALLINT":
		return "SMALLINT"
	case "DECIMAL", "NUMERIC":
		if column.Size > 0 {
			return fmt.Sprintf("DECIMAL(%d)", column.Size)
		}
		return "DECIMAL"
	case "FLOAT", "REAL":
		return "REAL"
	case "DOUBLE":
		return "DOUBLE PRECISION"
	case "VARCHAR":
		if column.Size > 0 {
			return fmt.Sprintf("VARCHAR(%d)", column.Size)
		}
		return "VARCHAR(255)"
	case "CHAR":
		if column.Size > 0 {
			return fmt.Sprintf("CHAR(%d)", column.Size)
		}
		return "CHAR(1)"
	case "TEXT", "CLOB":
		return "TEXT"
	case "BLOB", "BINARY", "VARBINARY":
		return "BYTEA"
	case "BOOLEAN", "BOOL":
		return "BOOLEAN"
	case "DATE":
		return "DATE"
	case "TIME":
		return "TIME"
	case "DATETIME", "TIMESTAMP":
		return "TIMESTAMP"
	case "JSON":
		return "JSON"
	case "JSONB":
		return "JSONB"
	case "UUID":
		return "UUID"
	default:
		// Check if the type contains size specification
		if strings.Contains(column.Type, "(") {
			return column.Type
		}
		return column.Type
	}
}
