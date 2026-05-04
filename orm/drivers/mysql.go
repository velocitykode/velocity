package drivers

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

// mysqlDSNPasswordRegex matches the password portion of a MySQL DSN between
// the first ':' after the optional username and the '@' that starts the host
// section. Password characters are URL-encoded so the capture ends at '@'.
var mysqlDSNPasswordRegex = regexp.MustCompile(`^([^:@/]+):[^@]*@`)

// redactMySQLDSN masks the password portion of a MySQL DSN so it can be
// embedded in error messages and logs without leaking credentials.
func redactMySQLDSN(dsn string) string {
	return mysqlDSNPasswordRegex.ReplaceAllString(dsn, `$1:[REDACTED]@`)
}

// resolveMySQLTLS returns the effective tls= value for a MySQL connection.
//
// Precedence:
//  1. Config.TLS, if set. Applications may pass "true", "skip-verify",
//     "preferred", or a named TLS profile registered via
//     github.com/go-sql-driver/mysql RegisterTLSConfig.
//  2. "preferred" — the secure default. Uses TLS if the server offers it,
//     otherwise falls back to plaintext.
func resolveMySQLTLS(configured string) string {
	if configured != "" {
		return configured
	}
	return "preferred"
}

// MySQLDriver implements the Driver interface for MySQL
type MySQLDriver struct {
	BaseDriver
}

// NewMySQLDriver creates a new MySQL driver instance
func NewMySQLDriver() Driver {
	return &MySQLDriver{}
}

// Connect establishes a connection to MySQL database
func (d *MySQLDriver) Connect(config ConnectionConfig) error {
	d.Config = config

	// Build DSN: user:password@tcp(host:port)/dbname
	// URL-encode username and password to handle special characters
	escapedUser := url.QueryEscape(config.Username)
	var dsn string
	if config.Password != "" {
		escapedPass := url.QueryEscape(config.Password)
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s",
			escapedUser,
			escapedPass,
			config.Host,
			config.Port,
			config.Database,
		)
	} else {
		dsn = fmt.Sprintf("%s@tcp(%s:%s)/%s",
			escapedUser,
			config.Host,
			config.Port,
			config.Database,
		)
	}

	// Add parameters
	params := []string{"parseTime=true"}
	if config.Charset != "" {
		params = append(params, "charset="+config.Charset)
	} else {
		params = append(params, "charset=utf8mb4")
	}
	if config.Collation != "" {
		params = append(params, "collation="+config.Collation)
	}
	if config.TimeZone != "" {
		params = append(params, "loc="+config.TimeZone)
	}
	// tls defaults to preferred — secure by default.
	params = append(params, "tls="+resolveMySQLTLS(config.TLS))

	dsn += "?" + strings.Join(params, "&")

	db, err := openAndPing("mysql", dsn)
	if err != nil {
		return fmt.Errorf("velocity/orm: mysql connect failed (dsn=%q): %w", redactMySQLDSN(dsn), err)
	}

	d.ConfigurePool(db)
	d.db = db
	return nil
}

// HasTable checks if a table exists
func (d *MySQLDriver) HasTable(name string) bool {
	sql := d.Grammar().CompileHasTable(name)
	var tableName string
	err := d.db.QueryRow(sql, name).Scan(&tableName)
	return err == nil && tableName == name
}

// HasColumn checks if a column exists in a table
func (d *MySQLDriver) HasColumn(table, column string) bool {
	sql := d.Grammar().CompileHasColumn(table, column)
	var count int
	err := d.db.QueryRow(sql, d.Config.Database, table, column).Scan(&count)
	return err == nil && count > 0
}

// CreateTable creates a new table
func (d *MySQLDriver) CreateTable(name string, definition func(*Table)) error {
	return d.CreateTableWith(d.Grammar(), name, definition)
}

// DropTable drops a table
func (d *MySQLDriver) DropTable(name string) error {
	return d.DropTableWith(d.Grammar(), name)
}

// Grammar returns the MySQL query grammar
func (d *MySQLDriver) Grammar() QueryGrammar {
	return &MySQLGrammar{}
}

// DriverName returns the driver name
func (d *MySQLDriver) DriverName() string {
	return "mysql"
}

// MySQLGrammar implements QueryGrammar for MySQL
type MySQLGrammar struct{}

// CompileSelect compiles a SELECT query for MySQL
func (g *MySQLGrammar) CompileSelect(query *SelectQuery) (string, []any) {
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
						sql.WriteString("?")
						args = append(args, values[j])
					}
					sql.WriteString(")")
				}
			case "BETWEEN":
				if values, ok := cond.Value.([]any); ok && len(values) == 2 {
					sql.WriteString(" ? AND ?")
					args = append(args, values[0], values[1])
				}
			default:
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
		sql.WriteString(fmt.Sprintf(" LIMIT %d", *query.Limit))
	}

	// OFFSET
	if query.Offset != nil {
		sql.WriteString(fmt.Sprintf(" OFFSET %d", *query.Offset))
	}

	// FOR UPDATE
	if query.LockForUpdate {
		sql.WriteString(" FOR UPDATE")
		if query.SkipLocked {
			sql.WriteString(" SKIP LOCKED")
		}
	}

	return sql.String(), args
}

// CompileInsert compiles an INSERT query for MySQL
func (g *MySQLGrammar) CompileInsert(table string, columns []string, values [][]any) (string, []any) {
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

// CompileUpdate compiles an UPDATE query for MySQL
func (g *MySQLGrammar) CompileUpdate(table string, values map[string]any, conditions []Condition) (string, []any) {
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
			if cond.Operator != "IS NULL" && cond.Operator != "IS NOT NULL" {
				sql.WriteString(" ?")
				args = append(args, cond.Value)
			}
		}
	}

	return sql.String(), args
}

// CompileDelete compiles a DELETE query for MySQL
func (g *MySQLGrammar) CompileDelete(table string, conditions []Condition) (string, []any) {
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
			if cond.Operator != "IS NULL" && cond.Operator != "IS NOT NULL" {
				sql.WriteString(" ?")
				args = append(args, cond.Value)
			}
		}
	}

	return sql.String(), args
}

// CompileCreateTable compiles a CREATE TABLE query for MySQL
func (g *MySQLGrammar) CompileCreateTable(name string, table *Table) string {
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
		sql.WriteString(g.getMySQLType(column))

		if column.AutoIncrement {
			sql.WriteString(" AUTO_INCREMENT")
		}
		if !column.Nullable {
			sql.WriteString(" NOT NULL")
		}
		if column.Unique {
			sql.WriteString(" UNIQUE")
		}
		if column.Primary {
			sql.WriteString(" PRIMARY KEY")
		}
		if column.Default != nil {
			sql.WriteString(" DEFAULT ")
			switch v := column.Default.(type) {
			case string:
				sql.WriteString(fmt.Sprintf("'%s'", v))
			case bool:
				if v {
					sql.WriteString("1")
				} else {
					sql.WriteString("0")
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

	sql.WriteString(") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci")

	return sql.String()
}

// CompileDropTable compiles a DROP TABLE query for MySQL
func (g *MySQLGrammar) CompileDropTable(name string) string {
	return fmt.Sprintf("DROP TABLE IF EXISTS %s", g.QuoteIdentifier(name))
}

// CompileHasTable compiles a query to check if table exists in MySQL
func (g *MySQLGrammar) CompileHasTable(name string) string {
	return "SHOW TABLES LIKE ?"
}

// CompileHasColumn compiles a query to check if column exists in MySQL
func (g *MySQLGrammar) CompileHasColumn(table, column string) string {
	return `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = ?
		AND table_name = ?
		AND column_name = ?`
}

// QuoteIdentifier quotes a database identifier for MySQL
func (g *MySQLGrammar) QuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// QuoteString quotes a string value for MySQL
func (g *MySQLGrammar) QuoteString(value string) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(value, "'", "''"))
}

// Placeholder returns the placeholder for prepared statements in MySQL
func (g *MySQLGrammar) Placeholder(index int) string {
	return "?"
}

// getMySQLType converts generic column types to MySQL types
func (g *MySQLGrammar) getMySQLType(column Column) string {
	switch strings.ToUpper(column.Type) {
	case "INT", "INTEGER":
		return "INT"
	case "BIGINT":
		return "BIGINT"
	case "SMALLINT":
		return "SMALLINT"
	case "TINYINT":
		return "TINYINT"
	case "DECIMAL", "NUMERIC":
		if column.Size > 0 {
			return fmt.Sprintf("DECIMAL(%d)", column.Size)
		}
		return "DECIMAL(10,2)"
	case "FLOAT", "REAL":
		return "FLOAT"
	case "DOUBLE":
		return "DOUBLE"
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
	case "LONGTEXT":
		return "LONGTEXT"
	case "BLOB", "BINARY", "VARBINARY":
		return "BLOB"
	case "BOOLEAN", "BOOL":
		return "TINYINT(1)"
	case "DATE":
		return "DATE"
	case "TIME":
		return "TIME"
	case "DATETIME":
		return "DATETIME"
	case "TIMESTAMP":
		return "TIMESTAMP"
	case "JSON":
		return "JSON"
	case "UUID":
		return "CHAR(36)"
	default:
		if strings.Contains(column.Type, "(") {
			return column.Type
		}
		return column.Type
	}
}
