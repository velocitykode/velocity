package migrate

import (
	"context"
	"fmt"
	"strings"
)

// allowedPostgresIndexMethods enumerates the index access methods that
// IndexBuilder.Using accepts for the postgres driver. The value is appended
// raw to the generated SQL, so the allowlist is the only thing standing
// between a caller and SQL injection via this field.
var allowedPostgresIndexMethods = map[string]struct{}{
	"btree":  {},
	"hash":   {},
	"gin":    {},
	"gist":   {},
	"brin":   {},
	"spgist": {},
}

// validatePostgresIndexMethod returns an error when method is not one of the
// supported postgres index access methods. Empty is allowed (caller decides
// whether to emit a USING clause).
func validatePostgresIndexMethod(method string) error {
	if method == "" {
		return nil
	}
	if _, ok := allowedPostgresIndexMethods[method]; !ok {
		return fmt.Errorf("invalid postgres index method %q: allowed values are btree, hash, gin, gist, brin, spgist", method)
	}
	return nil
}

// validatePartialIndexWhere validates a partial-index WHERE predicate against
// a deliberately narrow grammar. The expression is appended raw to the
// generated SQL, so anything that does not match the allowlist below must be
// rejected.
//
// Allowed tokens:
//   - whitespace
//   - identifiers matching ddlIdentifierRegex
//   - comparison/logical operators: = < > <= >= != <> AND OR NOT IN, plus the
//     compound forms IS NULL / IS NOT NULL
//   - integer and float literals (digits, optional leading sign, optional '.')
//   - single-quoted string literals with no embedded single quote, backslash,
//     or semicolon
//   - parentheses and commas
//
// Anything else (including ';', '--', '/*', '*/', '"', '`', backslash) is
// rejected. This is intentionally restrictive: users with complex partial
// index predicates should write a raw SQL migration rather than push them
// through this builder.
func validatePartialIndexWhere(where string) error {
	if where == "" {
		return nil
	}

	// Cheap structural rejects first so the per-token loop has fewer cases.
	for _, bad := range []string{";", "--", "/*", "*/", "\"", "`", "\\"} {
		if strings.Contains(where, bad) {
			return fmt.Errorf("invalid partial-index WHERE predicate: contains disallowed sequence %q", bad)
		}
	}

	i := 0
	n := len(where)
	for i < n {
		ch := where[i]

		// Whitespace
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			i++
			continue
		}

		// Parentheses / comma
		if ch == '(' || ch == ')' || ch == ',' {
			i++
			continue
		}

		// Single-quoted string literal: no embedded quote, backslash, or
		// semicolon. Backslash and semicolon were already rejected above,
		// so we only need to find the closing quote and confirm it exists.
		if ch == '\'' {
			j := i + 1
			for j < n && where[j] != '\'' {
				j++
			}
			if j >= n {
				return fmt.Errorf("invalid partial-index WHERE predicate: unterminated string literal")
			}
			i = j + 1
			continue
		}

		// Operators built from punctuation. Try the longer forms first.
		if ch == '<' || ch == '>' || ch == '=' || ch == '!' {
			// two-char ops: <=, >=, <>, !=
			if i+1 < n {
				two := where[i : i+2]
				if two == "<=" || two == ">=" || two == "<>" || two == "!=" {
					i += 2
					continue
				}
			}
			if ch == '<' || ch == '>' || ch == '=' {
				i++
				continue
			}
			return fmt.Errorf("invalid partial-index WHERE predicate: unexpected character %q", ch)
		}

		// Numeric literal: optional sign already covered by operators above,
		// so just digits with an optional decimal point.
		if ch >= '0' && ch <= '9' {
			j := i + 1
			sawDot := false
			for j < n {
				c := where[j]
				if c >= '0' && c <= '9' {
					j++
					continue
				}
				if c == '.' && !sawDot {
					sawDot = true
					j++
					continue
				}
				break
			}
			i = j
			continue
		}

		// Identifier / keyword. Use the same character class as
		// ddlIdentifierRegex: [A-Za-z_][A-Za-z0-9_]*. After consuming the
		// run, validate it explicitly against the regex so we keep a single
		// source of truth for identifier shape.
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			j := i + 1
			for j < n {
				c := where[j]
				if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' {
					j++
					continue
				}
				break
			}
			word := where[i:j]
			upper := strings.ToUpper(word)
			switch upper {
			case "AND", "OR", "NOT", "IN", "IS", "NULL", "TRUE", "FALSE":
				// recognised keyword
			default:
				if !ddlIdentifierRegex.MatchString(word) {
					return fmt.Errorf("invalid partial-index WHERE predicate: invalid identifier %q", word)
				}
			}
			i = j
			continue
		}

		return fmt.Errorf("invalid partial-index WHERE predicate: unexpected character %q", ch)
	}

	return nil
}

// IndexBuilder provides a fluent API for creating database indexes
type IndexBuilder struct {
	name        string
	table       string
	columns     []string
	unique      bool
	where       string   // Partial index condition (PostgreSQL, SQLite)
	include     []string // Covering index columns (PostgreSQL 11+)
	using       string   // Index type: btree, hash, gin, gist, brin, spgist (PostgreSQL)
	driver      string
	ifNotExists bool
	// err captures the first builder-side validation failure (e.g. an
	// invalid Using() method or Where() predicate). ToSQL surfaces it so
	// the caller sees a useful error at migration time even though the
	// fluent setters return *IndexBuilder for chaining.
	err error
}

// NewIndexBuilder creates a new IndexBuilder for the given index name,
// target table, and database driver ("postgres", "mysql", "sqlite"). Useful
// when assembling a CREATE INDEX statement outside the Migrator (for
// example in tests or external migration runners).
func NewIndexBuilder(name, table, driver string) *IndexBuilder {
	return &IndexBuilder{
		name:    name,
		table:   table,
		driver:  driver,
		columns: make([]string, 0),
		include: make([]string, 0),
	}
}

// Columns sets the columns to index
func (b *IndexBuilder) Columns(cols ...string) *IndexBuilder {
	b.columns = cols
	return b
}

// Unique marks the index as unique
func (b *IndexBuilder) Unique() *IndexBuilder {
	b.unique = true
	return b
}

// Where adds a partial index condition (PostgreSQL, SQLite).
//
// The condition is validated against a deliberately narrow grammar (see
// validatePartialIndexWhere): identifiers, integer/float literals, simple
// single-quoted string literals, comparison/logical operators, IS [NOT] NULL,
// IN, parentheses, and whitespace. Anything else (semicolons, comments,
// double quotes, backticks, backslashes) is rejected. Users with complex
// partial-index predicates should write a raw SQL migration instead.
//
// Example: Where("deleted_at IS NULL")
func (b *IndexBuilder) Where(condition string) *IndexBuilder {
	if err := validatePartialIndexWhere(condition); err != nil {
		if b.err == nil {
			b.err = err
		}
		return b
	}
	b.where = condition
	return b
}

// Include adds covering index columns (PostgreSQL 11+ only)
// These columns are stored in the index but not used for searching
// Example: Include("name", "email") for index-only scans
func (b *IndexBuilder) Include(cols ...string) *IndexBuilder {
	b.include = cols
	return b
}

// Using sets the index access method (PostgreSQL only).
// Supported: btree (default), hash, gin, gist, brin, spgist. Any other
// value is rejected at builder time and surfaces from ToSQL.
func (b *IndexBuilder) Using(indexType string) *IndexBuilder {
	if err := validatePostgresIndexMethod(indexType); err != nil {
		if b.err == nil {
			b.err = err
		}
		return b
	}
	b.using = indexType
	return b
}

// IfNotExists adds IF NOT EXISTS clause
func (b *IndexBuilder) IfNotExists() *IndexBuilder {
	b.ifNotExists = true
	return b
}

// ToSQL generates driver-specific CREATE INDEX SQL.
// Returns an error if any column or include identifier fails validation
// (must match ^[A-Za-z_][A-Za-z0-9_]*$), or if the builder captured a
// validation error from Using() / Where().
func (b *IndexBuilder) ToSQL() (string, error) {
	if b.err != nil {
		return "", b.err
	}
	// Defense in depth: re-validate raw fields at SQL generation time in
	// case a caller mutated them through reflection or a future setter.
	if err := validatePostgresIndexMethod(b.using); err != nil && b.driver == "postgres" {
		return "", err
	}
	if err := validatePartialIndexWhere(b.where); err != nil {
		return "", err
	}
	switch b.driver {
	case "postgres":
		return b.toPostgresSQL()
	case "mysql":
		return b.toMySQLSQL()
	case "sqlite":
		return b.toSQLiteSQL()
	default:
		return b.toSQLiteSQL()
	}
}

// quoteIdentifierList validates and quotes a slice of identifiers, returning
// them joined by ", ". Returns an error if any identifier fails validation
// against ddlIdentifierRegex.
func quoteIdentifierList(names []string, driver string) (string, error) {
	parts := make([]string, len(names))
	for i, n := range names {
		if !ddlIdentifierRegex.MatchString(n) {
			return "", fmt.Errorf("invalid identifier: %q", n)
		}
		parts[i] = quoteIdentifier(n, driver)
	}
	return strings.Join(parts, ", "), nil
}

func (b *IndexBuilder) toPostgresSQL() (string, error) {
	var sql strings.Builder

	sql.WriteString("CREATE ")
	if b.unique {
		sql.WriteString("UNIQUE ")
	}
	sql.WriteString("INDEX ")
	if b.ifNotExists {
		sql.WriteString("IF NOT EXISTS ")
	}
	sql.WriteString(quoteIdentifier(b.name, b.driver))
	sql.WriteString(" ON ")
	sql.WriteString(quoteIdentifier(b.table, b.driver))

	// Index type (USING)
	if b.using != "" {
		sql.WriteString(" USING ")
		sql.WriteString(b.using)
	}

	// Columns
	cols, err := quoteIdentifierList(b.columns, b.driver)
	if err != nil {
		return "", fmt.Errorf("invalid index column: %w", err)
	}
	sql.WriteString(" (")
	sql.WriteString(cols)
	sql.WriteString(")")

	// Include columns (PostgreSQL 11+)
	if len(b.include) > 0 {
		inc, err := quoteIdentifierList(b.include, b.driver)
		if err != nil {
			return "", fmt.Errorf("invalid include column: %w", err)
		}
		sql.WriteString(" INCLUDE (")
		sql.WriteString(inc)
		sql.WriteString(")")
	}

	// Partial index condition
	if b.where != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(b.where)
	}

	return sql.String(), nil
}

func (b *IndexBuilder) toMySQLSQL() (string, error) {
	var sql strings.Builder

	sql.WriteString("CREATE ")
	if b.unique {
		sql.WriteString("UNIQUE ")
	}
	sql.WriteString("INDEX ")
	sql.WriteString(quoteIdentifier(b.name, b.driver))
	sql.WriteString(" ON ")
	sql.WriteString(quoteIdentifier(b.table, b.driver))

	// Index type (MySQL uses different syntax)
	if b.using != "" && (b.using == "btree" || b.using == "hash") {
		sql.WriteString(" USING ")
		sql.WriteString(strings.ToUpper(b.using))
	}

	// Columns
	cols, err := quoteIdentifierList(b.columns, b.driver)
	if err != nil {
		return "", fmt.Errorf("invalid index column: %w", err)
	}
	sql.WriteString(" (")
	sql.WriteString(cols)
	sql.WriteString(")")

	// MySQL doesn't support INCLUDE or WHERE (partial indexes)
	// These are silently ignored

	return sql.String(), nil
}

func (b *IndexBuilder) toSQLiteSQL() (string, error) {
	var sql strings.Builder

	sql.WriteString("CREATE ")
	if b.unique {
		sql.WriteString("UNIQUE ")
	}
	sql.WriteString("INDEX ")
	if b.ifNotExists {
		sql.WriteString("IF NOT EXISTS ")
	}
	sql.WriteString(quoteIdentifier(b.name, b.driver))
	sql.WriteString(" ON ")
	sql.WriteString(quoteIdentifier(b.table, b.driver))

	// Columns
	cols, err := quoteIdentifierList(b.columns, b.driver)
	if err != nil {
		return "", fmt.Errorf("invalid index column: %w", err)
	}
	sql.WriteString(" (")
	sql.WriteString(cols)
	sql.WriteString(")")

	// Partial index condition (SQLite supports this)
	if b.where != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(b.where)
	}

	// SQLite doesn't support INCLUDE or USING
	// These are silently ignored

	return sql.String(), nil
}

// CreateIndex creates a new index using the fluent IndexBuilder API
func (m *Migrator) CreateIndex(name, table string, fn func(*IndexBuilder)) error {
	if !ddlIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid index name: %q", name)
	}
	if !ddlIdentifierRegex.MatchString(table) {
		return fmt.Errorf("invalid table name: %q", table)
	}

	builder := NewIndexBuilder(name, table, m.driver)
	fn(builder)

	sql, err := builder.ToSQL()
	if err != nil {
		return fmt.Errorf("failed to build index SQL for %s: %w", name, err)
	}
	if _, err := m.execContext(context.Background(), sql); err != nil {
		return fmt.Errorf("failed to create index %s: %w", name, err)
	}

	return nil
}

// DropIndex drops an index
func (m *Migrator) DropIndex(name string, table ...string) error {
	if !ddlIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid index name: %q", name)
	}
	quotedName := quoteIdentifier(name, m.driver)
	var sql string

	switch m.driver {
	case "postgres":
		sql = "DROP INDEX IF EXISTS " + quotedName
	case "mysql":
		// MySQL requires table name
		if len(table) == 0 {
			return fmt.Errorf("velocity/orm: mysql requires table name to drop index")
		}
		if !ddlIdentifierRegex.MatchString(table[0]) {
			return fmt.Errorf("invalid table name: %q", table[0])
		}
		sql = "DROP INDEX " + quotedName + " ON " + quoteIdentifier(table[0], m.driver)
	case "sqlite":
		sql = "DROP INDEX IF EXISTS " + quotedName
	default:
		sql = "DROP INDEX IF EXISTS " + quotedName
	}

	_, err := m.execContext(context.Background(), sql)
	if err != nil {
		return fmt.Errorf("failed to drop index %s: %w", name, err)
	}

	return nil
}

// Index is a shorthand for creating a simple index
// For more options, use CreateIndex with IndexBuilder
func (m *Migrator) Index(table string, columns ...string) error {
	name := fmt.Sprintf("idx_%s_%s", table, strings.Join(columns, "_"))
	return m.CreateIndex(name, table, func(b *IndexBuilder) {
		b.Columns(columns...)
	})
}

// UniqueIndex is a shorthand for creating a unique index
func (m *Migrator) UniqueIndex(table string, columns ...string) error {
	name := fmt.Sprintf("idx_%s_%s_unique", table, strings.Join(columns, "_"))
	return m.CreateIndex(name, table, func(b *IndexBuilder) {
		b.Columns(columns...).Unique()
	})
}
