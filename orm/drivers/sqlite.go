package drivers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/velocitykode/velocity/orm/internal/sqlitebackend"
)

// init installs the backend-selecting SQLite constructor on the internal
// seam so the cgo leaf (orm/sqlite) can build a "sqlite3"-backed driver
// without orm/drivers exporting that constructor publicly.
func init() {
	sqlitebackend.New = func(sqlDriver string) any {
		return &SQLiteDriver{sqlDriver: sqlDriver}
	}
}

// sqliteDirMode is the secret-tier permission applied to the directory
// holding the SQLite database file. The database carries user records,
// session state, queue payloads, and other framework tables, so it must
// not be world-readable on a multi-tenant host.
const sqliteDirMode os.FileMode = 0o700

// hasDotDotTraversal checks if a cleaned path still contains ".." traversal components.
func hasDotDotTraversal(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// SQLiteDriver implements the Driver interface for SQLite. The dial logic
// (DSN building, directory creation, PRAGMAs) is dialect-only and stdlib-bound;
// the single thing that varies between the pure-Go and cgo backends is the
// database/sql driver name to open, carried in sqlDriver. This lets the same
// connector back both the always-on modernc default ("sqlite") in the orm root
// and the opt-in cgo leaf ("sqlite3", orm/sqlite) without duplicating the
// security-sensitive directory-permission logic.
type SQLiteDriver struct {
	BaseDriver
	// sqlDriver is the database/sql driver name passed to sql.Open. Set by
	// NewSQLiteDriver (pure-Go "sqlite") or, for the cgo leaf, the
	// sqlitebackend seam ("sqlite3"); empty is treated as the pure-Go
	// default "sqlite".
	sqlDriver string
}

// NewSQLiteDriver creates a new SQLite driver instance backed by the pure-Go
// modernc database/sql driver ("sqlite"). The modernc driver must be
// registered with database/sql (it self-registers from a blank import in the
// orm root) for Connect to succeed.
func NewSQLiteDriver() Driver {
	return &SQLiteDriver{sqlDriver: "sqlite"}
}

// sqlDriverName returns the database/sql driver name to open, defaulting to
// the pure-Go "sqlite" when the driver was constructed without one.
func (d *SQLiteDriver) sqlDriverName() string {
	if d.sqlDriver == "" {
		return "sqlite"
	}
	return d.sqlDriver
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

	// Create directory if it doesn't exist (for file-based databases).
	// The directory will hold the SQLite file containing user records,
	// session state, queue payloads, and every other framework table; it
	// must not be world-readable on a multi-tenant host.
	if dsn != ":memory:" && strings.Contains(dsn, "/") {
		dir := filepath.Dir(dsn)
		if err := os.MkdirAll(dir, sqliteDirMode); err != nil {
			return fmt.Errorf("failed to create database directory: %w", err)
		}
		// MkdirAll preserves the perms of a pre-existing directory. Force
		// the tight mode on every open so an older binary's 0o755 dir does
		// not stay world-readable across upgrades.
		if err := os.Chmod(dir, sqliteDirMode); err != nil {
			return fmt.Errorf("failed to tighten database directory permissions: %w", err)
		}
	}

	// No time-related DSN parameters: the time codec stays at the
	// backend default so time.Time params (rebased to UTC by
	// NormalizeTimeArgs before binding) are stored with a UTC wall
	// clock. The old `_loc=` param was a mattn-only codec knob - the
	// default modernc backend silently ignored it - and codec knobs
	// that vary by config are exactly how storage guarantees die.
	// SQLite has no session-timezone concept, so ConnectionConfig.
	// TimeZone is intentionally unused here.

	db, err := sql.Open(d.sqlDriverName(), dsn)
	if err != nil {
		return err
	}

	// Configure connection pool. d.Config was set at the top of Connect, so
	// ConfigurePool sees the same values and also applies ConnMaxIdleTime and
	// the >0 guards the inline version dropped.
	d.ConfigurePool(db)

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
	// CompileHasColumn bakes the quoted table name into the PRAGMA, so the
	// statement carries zero placeholders; passing a bind arg here is a bug.
	rows, err := d.db.Query(sql)
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

// ListTables returns user tables in the connected SQLite database.
func (d *SQLiteDriver) ListTables(ctx context.Context) ([]string, error) {
	grammar, ok := d.Grammar().(IntrospectionGrammar)
	if !ok {
		return nil, fmt.Errorf("sqlite grammar does not support schema introspection")
	}
	rows, err := d.QueryContext(ctx, grammar.CompileListTables())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}

// DescribeTable returns column metadata for a SQLite table in ordinal order.
func (d *SQLiteDriver) DescribeTable(ctx context.Context, table string) ([]ColumnSchema, error) {
	if err := ValidateSchemaIdentifier(table); err != nil {
		return nil, err
	}

	grammar, ok := d.Grammar().(IntrospectionGrammar)
	if !ok {
		return nil, fmt.Errorf("sqlite grammar does not support schema introspection")
	}
	rows, err := d.QueryContext(ctx, grammar.CompileDescribeTable(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns := make([]ColumnSchema, 0)
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notnull int
		var dfltValue *string
		var pk int

		if err := rows.Scan(&cid, &name, &typ, &notnull, &dfltValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, ColumnSchema{
			Name:       name,
			DataType:   typ,
			Nullable:   notnull == 0 && pk == 0,
			Default:    dfltValue,
			PrimaryKey: pk > 0,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("sqlite table %q not found", table)
	}
	return columns, nil
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

// OperatorRegistry returns nil. SQLite gains no extension operators in this
// release; the seam is in place for json1 / fts5 follow-ups.
func (d *SQLiteDriver) OperatorRegistry() map[string]OperatorSpec {
	return nil
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

	// Columns. Defence-in-depth: re-validate every projection here
	// even though Query[T].Select rejects them upstream. Any code path
	// that constructs SelectQuery directly (tests, future call sites)
	// must not bypass the whitelist. RawColumns is the trusted escape
	// hatch and is emitted separately below.
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
			// Raw-expression ordering: SQLite uses "?" placeholders verbatim, so
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
			// Raw SQL values (e.g. orm.CurrentTimestamp) emit as SQL
			// expressions instead of binding, matching CompileUpdate.
			if raw, ok := row[j].(RawSQL); ok {
				sql.WriteString(sqliteRawSQLExpr(raw))
				continue
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

		// Raw SQL values (e.g. CURRENT_TIMESTAMP) emit verbatim, with the
		// well-known current-timestamp sentinels mapped to SQLite's
		// CURRENT_TIMESTAMP (already UTC); all other values bind as
		// parameters.
		if rawVal, ok := value.(RawSQL); ok {
			sql.WriteString(" = ")
			sql.WriteString(sqliteRawSQLExpr(rawVal))
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

// CompileDelete compiles a DELETE query
func (g *SQLiteGrammar) CompileDelete(table string, conditions []Condition) (string, []any) {
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
// appending bound parameters to args. SQLite uses positional `?`
// placeholders; no index threading is required.
//
// Conditions with non-empty Group are rendered as parenthesized
// sub-groups, recursively. The conjunction (AND/OR) for a sub-group is
// taken from cond.Type, identical to the leaf-condition behaviour.
func (g *SQLiteGrammar) compileConditions(sql *strings.Builder, args *[]any, conditions []Condition) {
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
		// built-in switch. SQLite's OperatorRegistry returns nil today so
		// this branch is dead, but the seam stays in place for json1 / fts5
		// follow-ups that need the same template machinery.
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
			switch v := column.Default.(type) {
			case string:
				sql.WriteString(g.QuoteString(v))
			default:
				sql.WriteString(fmt.Sprintf("%v", v))
			}
		}
	}

	sql.WriteString(")")

	return sql.String()
}

// CompileCreateIndexes compiles each Table.Index into a standalone SQLite
// CREATE [UNIQUE] INDEX statement. SQLite has no inline INDEX clause inside
// CREATE TABLE, so CreateTableWith runs these after the table statement.
func (g *SQLiteGrammar) CompileCreateIndexes(name string, table *Table) []string {
	if len(table.Indexes) == 0 {
		return nil
	}
	stmts := make([]string, 0, len(table.Indexes))
	for _, index := range table.Indexes {
		var sql strings.Builder
		sql.WriteString("CREATE ")
		if index.Unique {
			sql.WriteString("UNIQUE ")
		}
		sql.WriteString("INDEX ")
		sql.WriteString(g.QuoteIdentifier(index.Name))
		sql.WriteString(" ON ")
		sql.WriteString(g.QuoteIdentifier(name))
		sql.WriteString(" (")
		for j, col := range index.Columns {
			if j > 0 {
				sql.WriteString(", ")
			}
			sql.WriteString(g.QuoteIdentifier(col))
		}
		sql.WriteString(")")
		stmts = append(stmts, sql.String())
	}
	return stmts
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

// CompileListTables compiles a query to list user tables in SQLite.
func (g *SQLiteGrammar) CompileListTables() string {
	return "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name"
}

// CompileDescribeTable compiles a query to describe columns in a SQLite table.
func (g *SQLiteGrammar) CompileDescribeTable(table string) string {
	return fmt.Sprintf("PRAGMA table_info(%s)", g.QuoteIdentifier(table))
}

// QuoteIdentifier quotes a database identifier.
// Dot-qualified names are quoted per segment: users.email -> `users`.`email`.
func (g *SQLiteGrammar) QuoteIdentifier(name string) string {
	return quoteQualified(name, func(seg string) string {
		return "`" + strings.ReplaceAll(seg, "`", "``") + "`"
	})
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
