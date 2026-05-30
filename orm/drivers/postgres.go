package drivers

import (
	"fmt"
	"strings"
)

// PostgresGrammar implements QueryGrammar for PostgreSQL.
//
// The grammar is stdlib-only and stays in orm/drivers so the ~40 internal
// dialect tests and any dialect consumer compile without pulling in lib/pq.
// The heavy connector (PostgresDriver, DSN escaping, lib/pq) lives in the
// orm/postgres leaf package.
type PostgresGrammar struct{}

// CompileSelect compiles a SELECT query for PostgreSQL
func (g *PostgresGrammar) CompileSelect(query *SelectQuery) (string, []any) {
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
	// hatch and is emitted separately below, with "?" placeholders
	// renumbered to PostgreSQL's $N form.
	wroteCol := false
	if len(query.Columns) > 0 {
		for _, col := range query.Columns {
			if err := ValidateSelectColumn(col); err != nil {
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
		rewritten, _ := rewriteQuestionMarksToDollar(raw.Expr, len(args)+1)
		sql.WriteString(rewritten)
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
		// Args may already contain RawColumn parameters bound in
		// the projection list, so start WHERE's $N counter from
		// the current parameter count rather than 1.
		argIndex := len(args) + 1
		argIndex = g.compileConditions(&sql, &args, query.Conditions, argIndex)
		_ = argIndex
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
			switch cond.Operator {
			case "IS NULL", "IS NOT NULL":
				// No placeholder needed
			default:
				sql.WriteString(fmt.Sprintf(" $%d", argIndex))
				args = append(args, cond.Value)
				argIndex++
			}
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

		// Raw SQL values (e.g. orm.NOW) emit verbatim; all other values bind.
		if rawVal, ok := value.(RawSQL); ok {
			sql.WriteString(" = ")
			sql.WriteString(string(rawVal))
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
		argIndex = g.compileConditions(&sql, &args, conditions, argIndex)
		_ = argIndex
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
		argIndex = g.compileConditions(&sql, &args, conditions, argIndex)
		_ = argIndex
	}

	return sql.String(), args
}

// CompileUpdateReturning compiles an UPDATE that appends RETURNING <pkCol>
// to the statement so the affected primary keys can be captured atomically
// alongside the write. Used by the ORM's bulk-hook surface to eliminate
// the pre-SELECT race window that the SQLite/MySQL path still relies on.
//
// pkCol is quoted through the grammar's identifier quoter; callers must
// have validated it as a real column name first (the ORM resolves it from
// model meta, not user input).
//
// The returned SQL/args are otherwise identical to CompileUpdate, so the
// caller switches Exec vs Query based on whether they need to scan the
// RETURNING rowset.
func (g *PostgresGrammar) CompileUpdateReturning(table string, values map[string]any, conditions []Condition, pkCol string) (string, []any) {
	sql, args := g.CompileUpdate(table, values, conditions)
	return sql + " RETURNING " + g.QuoteIdentifier(pkCol), args
}

// CompileDeleteReturning compiles a DELETE that appends RETURNING <pkCol>
// to the statement so the deleted primary keys can be captured atomically
// alongside the write. Counterpart to CompileUpdateReturning; see that
// method for the full rationale.
func (g *PostgresGrammar) CompileDeleteReturning(table string, conditions []Condition, pkCol string) (string, []any) {
	sql, args := g.CompileDelete(table, conditions)
	return sql + " RETURNING " + g.QuoteIdentifier(pkCol), args
}

// compileConditions renders a list of WHERE/HAVING conditions into sql,
// appending bound parameters to args. argIndex is the next 1-based
// PostgreSQL placeholder ($N) to allocate; the returned int is the next
// free placeholder after this list.
//
// Conditions with non-empty Group are rendered as parenthesized
// sub-groups, recursively. The conjunction (AND/OR) for a sub-group is
// taken from cond.Type, identical to the leaf-condition behaviour.
func (g *PostgresGrammar) compileConditions(sql *strings.Builder, args *[]any, conditions []Condition, argIndex int) int {
	for i, cond := range conditions {
		if i > 0 {
			sql.WriteString(" ")
			sql.WriteString(strings.ToUpper(cond.Type))
			sql.WriteString(" ")
		}

		// Sub-group: emit (<inner>) recursively.
		if len(cond.Group) > 0 {
			sql.WriteString("(")
			argIndex = g.compileConditions(sql, args, cond.Group, argIndex)
			sql.WriteString(")")
			continue
		}

		// Driver-registered operator: render Spec.Template instead of the
		// built-in switch. Placeholders ({{lhs}}, {{op}}, {{rhs}}) absorb
		// the column, operator literal, and bound-parameter form so dialect
		// quirks (e.g. JSONB cast) live in the template, not the call site.
		if cond.Spec != nil {
			fragment, newIdx := renderOperatorTemplate(g, cond, argIndex, args, "$%d")
			sql.WriteString(fragment)
			argIndex = newIdx
			continue
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
					*args = append(*args, values[j])
				}
				sql.WriteString(")")
			}
		case "BETWEEN":
			if values, ok := cond.Value.([]any); ok && len(values) == 2 {
				sql.WriteString(fmt.Sprintf(" $%d AND $%d", argIndex, argIndex+1))
				*args = append(*args, values[0], values[1])
				argIndex += 2
			}
		default:
			sql.WriteString(fmt.Sprintf(" $%d", argIndex))
			*args = append(*args, cond.Value)
			argIndex++
		}
	}
	return argIndex
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

// rewriteQuestionMarksToDollar walks expr and replaces every unquoted
// "?" with PostgreSQL's $N placeholder starting from startIdx. It
// returns the rewritten expression and the next available index.
//
// SelectRaw expressions are NOT parsed as SQL; this rewriter only
// recognises single-quoted and double-quoted regions to avoid
// substituting question marks that appear inside literals/identifiers.
// Backslash escapes inside quotes are not interpreted (PostgreSQL uses
// doubled quotes for escaping, which leaves and re-enters the quoted
// region naturally).
func rewriteQuestionMarksToDollar(expr string, startIdx int) (string, int) {
	if !strings.ContainsRune(expr, '?') {
		return expr, startIdx
	}
	var b strings.Builder
	b.Grow(len(expr) + 4)
	idx := startIdx
	inSingle := false
	inDouble := false
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(c)
		case c == '?' && !inSingle && !inDouble:
			b.WriteByte('$')
			b.WriteString(fmt.Sprintf("%d", idx))
			idx++
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), idx
}
