package drivers

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/lib/pq"
)

// PostgresDriver implements the Driver interface for PostgreSQL
type PostgresDriver struct {
	db     *sql.DB
	config ConnectionConfig
}

// NewPostgresDriver creates a new PostgreSQL driver instance
func NewPostgresDriver() Driver {
	return &PostgresDriver{}
}

// Connect establishes a connection to PostgreSQL database
func (d *PostgresDriver) Connect(config ConnectionConfig) error {
	d.config = config

	// Build DSN (PostgreSQL connection string)
	dsn := fmt.Sprintf("host=%s port=%s user=%s dbname=%s",
		config.Host,
		config.Port,
		config.Username,
		config.Database,
	)

	// Add password only if provided
	if config.Password != "" {
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s",
			config.Host,
			config.Port,
			config.Username,
			config.Password,
			config.Database,
		)
	}

	// Add optional parameters
	if config.SSLMode != "" {
		dsn += " sslmode=" + config.SSLMode
	} else {
		dsn += " sslmode=disable" // Default to disable for development
	}

	if config.TimeZone != "" {
		dsn += " TimeZone=" + config.TimeZone
	}

	if config.Schema != "" {
		dsn += " search_path=" + config.Schema
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Configure connection pool
	if config.MaxIdleConns > 0 {
		db.SetMaxIdleConns(config.MaxIdleConns)
	}
	if config.MaxOpenConns > 0 {
		db.SetMaxOpenConns(config.MaxOpenConns)
	}
	if config.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(config.ConnMaxLifetime)
	}
	if config.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(config.ConnMaxIdleTime)
	}

	d.db = db
	return nil
}

// Close closes the database connection
func (d *PostgresDriver) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// Ping verifies the connection to the database
func (d *PostgresDriver) Ping() error {
	if d.db == nil {
		return fmt.Errorf("no database connection")
	}
	return d.db.Ping()
}

// DB returns the underlying *sql.DB instance
func (d *PostgresDriver) DB() *sql.DB {
	return d.db
}

// Query executes a query that returns rows
func (d *PostgresDriver) Query(query string, args ...any) (*sql.Rows, error) {
	if d.config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: %v\n", query, args)
	}
	return d.db.Query(query, args...)
}

// QueryRow executes a query that returns at most one row
func (d *PostgresDriver) QueryRow(query string, args ...any) *sql.Row {
	if d.config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: %v\n", query, args)
	}
	return d.db.QueryRow(query, args...)
}

// Exec executes a query that doesn't return rows
func (d *PostgresDriver) Exec(query string, args ...any) (sql.Result, error) {
	if d.config.LogQueries {
		fmt.Printf("SQL: %s\nArgs: %v\n", query, args)
	}
	return d.db.Exec(query, args...)
}

// Begin starts a transaction
func (d *PostgresDriver) Begin() (*sql.Tx, error) {
	return d.db.Begin()
}

// BeginTx starts a transaction with options
func (d *PostgresDriver) BeginTx() (*sql.Tx, error) {
	return d.db.Begin()
}

// CreateTable creates a new table
func (d *PostgresDriver) CreateTable(name string, definition func(*Table)) error {
	table := &Table{Name: name}
	definition(table)

	sql := d.Grammar().CompileCreateTable(name, table)
	_, err := d.db.Exec(sql)
	return err
}

// DropTable drops a table
func (d *PostgresDriver) DropTable(name string) error {
	sql := d.Grammar().CompileDropTable(name)
	_, err := d.db.Exec(sql)
	return err
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
		if column.AutoIncrement {
			// PostgreSQL uses SERIAL for auto-increment
			// Type should already be set to SERIAL
		}
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
	return fmt.Sprintf(`"%s"`, name)
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
