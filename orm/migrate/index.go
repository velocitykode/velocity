package migrate

import (
	"fmt"
	"strings"
)

// IndexBuilder provides a fluent API for creating database indexes
type IndexBuilder struct {
	name        string
	table       string
	columns     []string
	unique      bool
	where       string   // Partial index condition (PostgreSQL, SQLite)
	include     []string // Covering index columns (PostgreSQL 11+)
	using       string   // Index type: btree, hash, gin, gist, brin (PostgreSQL)
	driver      string
	ifNotExists bool
}

// newIndexBuilder creates a new IndexBuilder
func newIndexBuilder(name, table, driver string) *IndexBuilder {
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

// Where adds a partial index condition (PostgreSQL, SQLite)
// Example: Where("deleted_at IS NULL")
func (b *IndexBuilder) Where(condition string) *IndexBuilder {
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

// Using sets the index type (PostgreSQL only)
// Supported: btree (default), hash, gin, gist, brin
func (b *IndexBuilder) Using(indexType string) *IndexBuilder {
	b.using = indexType
	return b
}

// IfNotExists adds IF NOT EXISTS clause
func (b *IndexBuilder) IfNotExists() *IndexBuilder {
	b.ifNotExists = true
	return b
}

// ToSQL generates driver-specific CREATE INDEX SQL
func (b *IndexBuilder) ToSQL() string {
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

func (b *IndexBuilder) toPostgresSQL() string {
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
	sql.WriteString(" (")
	sql.WriteString(strings.Join(b.columns, ", "))
	sql.WriteString(")")

	// Include columns (PostgreSQL 11+)
	if len(b.include) > 0 {
		sql.WriteString(" INCLUDE (")
		sql.WriteString(strings.Join(b.include, ", "))
		sql.WriteString(")")
	}

	// Partial index condition
	if b.where != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(b.where)
	}

	return sql.String()
}

func (b *IndexBuilder) toMySQLSQL() string {
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
	sql.WriteString(" (")
	sql.WriteString(strings.Join(b.columns, ", "))
	sql.WriteString(")")

	// MySQL doesn't support INCLUDE or WHERE (partial indexes)
	// These are silently ignored

	return sql.String()
}

func (b *IndexBuilder) toSQLiteSQL() string {
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
	sql.WriteString(" (")
	sql.WriteString(strings.Join(b.columns, ", "))
	sql.WriteString(")")

	// Partial index condition (SQLite supports this)
	if b.where != "" {
		sql.WriteString(" WHERE ")
		sql.WriteString(b.where)
	}

	// SQLite doesn't support INCLUDE or USING
	// These are silently ignored

	return sql.String()
}

// CreateIndex creates a new index using the fluent IndexBuilder API
func (m *Migrator) CreateIndex(name, table string, fn func(*IndexBuilder)) error {
	builder := newIndexBuilder(name, table, m.driver)
	fn(builder)

	sql := builder.ToSQL()
	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to create index %s: %w", name, err)
	}

	return nil
}

// DropIndex drops an index
func (m *Migrator) DropIndex(name string, table ...string) error {
	quotedName := quoteIdentifier(name, m.driver)
	var sql string

	switch m.driver {
	case "postgres":
		sql = "DROP INDEX IF EXISTS " + quotedName
	case "mysql":
		// MySQL requires table name
		if len(table) == 0 {
			return fmt.Errorf("MySQL requires table name to drop index")
		}
		sql = "DROP INDEX " + quotedName + " ON " + quoteIdentifier(table[0], m.driver)
	case "sqlite":
		sql = "DROP INDEX IF EXISTS " + quotedName
	default:
		sql = "DROP INDEX IF EXISTS " + quotedName
	}

	_, err := m.db.Exec(sql)
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
