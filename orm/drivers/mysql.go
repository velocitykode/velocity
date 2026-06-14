package drivers

import (
	"fmt"
	"strings"
)

// MySQLGrammar implements QueryGrammar for MySQL.
//
// The grammar is stdlib-only and stays in orm/drivers so the internal dialect
// tests compile without pulling in go-sql-driver/mysql. The heavy connector
// (MySQLDriver, DSN building, the mysql driver) lives in the orm/mysql leaf.
type MySQLGrammar struct{}

// CompileSelect compiles a SELECT query for MySQL
func (g *MySQLGrammar) CompileSelect(query *SelectQuery) (string, []any) {
	var sql strings.Builder
	var args []any

	sql.WriteString("SELECT ")

	if query.Distinct {
		sql.WriteString("DISTINCT ")
	}

	// Columns. Defence-in-depth: re-validate every projection here
	// even though Query[T].Select rejects them upstream. Any code path
	// that constructs SelectQuery directly (tests, future call sites)
	// must not bypass the whitelist. RawColumns is the trusted escape
	// hatch and is emitted separately below.
	wroteCol := false
	if len(query.Columns) > 0 {
		for _, col := range query.Columns {
			if err := ValidateSelectColumn(col); err != nil {
				// Compile-time rejection of an injection sink:
				// emit a syntactically invalid statement so
				// the database returns an error rather than
				// running anything. The query never reaches
				// the wire intact.
				return "/* orm: rejected select column: " + sanitizeForComment(err.Error()) + " */ SELECT 1 WHERE 1=0", nil
			}
			if wroteCol {
				sql.WriteString(", ")
			}
			if strings.Contains(col, "(") || col == "*" {
				sql.WriteString(col)
			} else {
				sql.WriteString(g.QuoteIdentifier(col))
			}
			wroteCol = true
		}
	}
	for _, raw := range query.RawColumns {
		if wroteCol {
			sql.WriteString(", ")
		}
		sql.WriteString(raw.Expr)
		args = append(args, raw.Args...)
		wroteCol = true
	}
	if !wroteCol {
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
		g.compileConditions(&sql, &args, query.Conditions)
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

	// HAVING: same condition machinery as WHERE so IN lists, BETWEEN
	// and sub-groups compile identically in both clauses.
	if len(query.Having) > 0 {
		sql.WriteString(" HAVING ")
		g.compileConditions(&sql, &args, query.Having)
	}

	// ORDER BY
	if len(query.Orders) > 0 {
		sql.WriteString(" ORDER BY ")
		for i, order := range query.Orders {
			if i > 0 {
				sql.WriteString(", ")
			}
			// Raw-expression ordering: MySQL uses "?" placeholders verbatim, so
			// the Expr is emitted as-is and its Args appended after the
			// WHERE/HAVING args to keep positional binding contiguous.
			if order.Expr != "" {
				sql.WriteString(order.Expr)
				args = append(args, order.Args...)
				if order.Direction != "" {
					sql.WriteString(" ")
					sql.WriteString(order.Direction)
				}
				continue
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
		g.compileConditions(&sql, &args, conditions)
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
		g.compileConditions(&sql, &args, conditions)
	}

	return sql.String(), args
}

// compileConditions renders a list of WHERE/HAVING conditions into sql,
// appending bound parameters to args. MySQL uses positional `?`
// placeholders; no index threading is required.
//
// Conditions with non-empty Group are rendered as parenthesized
// sub-groups, recursively. The conjunction (AND/OR) for a sub-group is
// taken from cond.Type, identical to the leaf-condition behaviour.
func (g *MySQLGrammar) compileConditions(sql *strings.Builder, args *[]any, conditions []Condition) {
	for i, cond := range conditions {
		if i > 0 {
			sql.WriteString(" ")
			sql.WriteString(strings.ToUpper(cond.Type))
			sql.WriteString(" ")
		}

		// Sub-group: emit (<inner>) recursively.
		if len(cond.Group) > 0 {
			sql.WriteString("(")
			g.compileConditions(sql, args, cond.Group)
			sql.WriteString(")")
			continue
		}

		// Driver-registered operator: render Spec.Template instead of the
		// built-in switch. MySQL's OperatorRegistry returns nil today so
		// this branch is dead, but the seam stays in place for the
		// JSON_CONTAINS / JSON_OVERLAPS follow-ups.
		if cond.Spec != nil {
			fragment, _ := renderOperatorTemplate(g, cond, 0, args, questionPlaceholder)
			sql.WriteString(fragment)
			continue
		}

		// Empty IN/NOT IN list is invalid SQL ("col IN ()" parses but
		// behaves inconsistently across engines). Emit a constant boolean
		// instead so the predicate is well-formed and produces the
		// semantically correct result:
		//   WhereIn(col, [])    -> 1=0  (never matches)
		//   WhereNotIn(col, []) -> 1=1  (always matches)
		if cond.Operator == "IN" || cond.Operator == "NOT IN" {
			values, _ := cond.Value.([]any)
			if len(values) == 0 {
				if cond.Operator == "IN" {
					sql.WriteString("1 = 0")
				} else {
					sql.WriteString("1 = 1")
				}
				continue
			}
		}

		sql.WriteString(g.QuoteIdentifier(cond.Column))
		sql.WriteString(" ")
		sql.WriteString(cond.Operator)

		switch cond.Operator {
		case "IS NULL", "IS NOT NULL":
			// No placeholder needed
		case "IN", "NOT IN":
			// Empty-list case handled above; here len(values) > 0.
			values, _ := cond.Value.([]any)
			sql.WriteString(" (")
			for j := range values {
				if j > 0 {
					sql.WriteString(", ")
				}
				sql.WriteString("?")
				*args = append(*args, values[j])
			}
			sql.WriteString(")")
		case "BETWEEN", "NOT BETWEEN":
			if values, ok := cond.Value.([]any); ok && len(values) == 2 {
				sql.WriteString(" ? AND ?")
				*args = append(*args, values[0], values[1])
			}
		default:
			sql.WriteString(" ?")
			*args = append(*args, cond.Value)
		}
	}
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
				sql.WriteString(g.QuoteString(v))
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

// CompileListTables compiles a query to list user tables in a MySQL database.
func (g *MySQLGrammar) CompileListTables() string {
	return `SELECT t.table_name
		FROM information_schema.tables AS t
		WHERE t.table_schema = ?
		AND t.table_type = 'BASE TABLE'
		ORDER BY t.table_name`
}

// CompileDescribeTable compiles a query to describe columns in a MySQL table.
func (g *MySQLGrammar) CompileDescribeTable(_ string) string {
	return `SELECT
			c.column_name,
			c.column_type,
			c.is_nullable,
			c.column_default,
			c.column_key
		FROM information_schema.columns AS c
		WHERE c.table_schema = ?
		AND c.table_name = ?
		ORDER BY c.ordinal_position`
}

// QuoteIdentifier quotes a database identifier for MySQL.
// Dot-qualified names are quoted per segment: users.email -> `users`.`email`.
func (g *MySQLGrammar) QuoteIdentifier(name string) string {
	return quoteQualified(name, func(seg string) string {
		return "`" + strings.ReplaceAll(seg, "`", "``") + "`"
	})
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
