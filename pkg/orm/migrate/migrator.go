package migrate

import (
	"database/sql"
	"errors"
	"fmt"
)

// Migrator handles migration execution for a specific database connection
type Migrator struct {
	db             *sql.DB
	driver         string
	migrationsPath string
}

// NewMigrator creates a new Migrator instance
func NewMigrator(db *sql.DB, driver string) *Migrator {
	if db == nil {
		panic("database connection cannot be nil")
	}

	if driver == "" {
		panic("driver name cannot be empty")
	}

	return &Migrator{
		db:             db,
		driver:         driver,
		migrationsPath: "migrations",
	}
}

// SetMigrationsPath sets the path to migration files
func (m *Migrator) SetMigrationsPath(path string) {
	m.migrationsPath = path
}

// Up runs all pending migrations
func (m *Migrator) Up() error {
	// Ensure migrations table exists
	if err := m.createMigrationsTable(); err != nil {
		return err
	}

	// Get applied migrations
	appliedVersions, err := m.getAppliedMigrations()
	if err != nil {
		return err
	}

	// Build set of applied versions
	appliedSet := make(map[string]bool)
	for _, version := range appliedVersions {
		appliedSet[version] = true
	}

	// Get all registered migrations
	all := globalRegistry.All()

	// Find pending migrations
	pending := make([]Migration, 0)
	for _, migration := range all {
		if !appliedSet[migration.Version] {
			pending = append(pending, migration)
		}
	}

	if len(pending) == 0 {
		return nil // Nothing to migrate
	}

	// Get next batch number
	lastBatch, err := m.getLastBatch()
	if err != nil {
		return err
	}
	nextBatch := lastBatch + 1

	// Execute each pending migration
	for _, migration := range pending {
		if err := migration.Up(m); err != nil {
			return err // Stop on first failure
		}

		// Record successful migration
		if err := m.recordMigration(migration.Version, nextBatch); err != nil {
			return err
		}
	}

	return nil
}

// Down rolls back the last N batches of migrations
func (m *Migrator) Down(steps int) error {
	if steps <= 0 {
		steps = 1 // Default to rolling back one batch
	}

	// Get last batch number
	lastBatch, err := m.getLastBatch()
	if err != nil {
		return err
	}

	if lastBatch == 0 {
		return nil // No migrations to rollback
	}

	// Roll back N batches
	for i := 0; i < steps && lastBatch > 0; i++ {
		// Get migrations in this batch
		versions, err := m.getMigrationsByBatch(lastBatch)
		if err != nil {
			return err
		}

		// Execute Down() for each migration in reverse order
		for _, version := range versions {
			migration, err := globalRegistry.Find(version)
			if err != nil {
				return err
			}

			if migration.Down == nil {
				return errors.New("migration " + version + " does not have a Down method")
			}

			if err := migration.Down(m); err != nil {
				return err // Stop on first failure
			}

			// Remove migration record
			if err := m.removeMigration(version); err != nil {
				return err
			}
		}

		lastBatch--
	}

	return nil
}

// Fresh drops all tables and re-runs all migrations
func (m *Migrator) Fresh() error {
	// Get all table names
	tables, err := m.getAllTables()
	if err != nil {
		return fmt.Errorf("failed to get tables: %w", err)
	}

	// Drop all tables
	for _, table := range tables {
		if err := m.dropTable(table); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	// Run all migrations
	return m.Up()
}

// Status returns the status of all migrations
func (m *Migrator) Status() ([]MigrationStatus, error) {
	// Ensure migrations table exists
	if err := m.createMigrationsTable(); err != nil {
		return nil, err
	}

	// Get applied migrations with batch info
	applied, err := m.getAppliedMigrationsWithBatch()
	if err != nil {
		return nil, err
	}

	// Get all registered migrations
	all := globalRegistry.All()

	// Build status list
	statuses := make([]MigrationStatus, 0, len(all))
	for _, migration := range all {
		status := MigrationStatus{
			Version: migration.Version,
		}

		if batch, exists := applied[migration.Version]; exists {
			status.State = "Applied"
			status.Batch = batch
		} else {
			status.State = "Pending"
			status.Batch = 0
		}

		statuses = append(statuses, status)
	}

	return statuses, nil
}

// MigrationStatus represents the status of a single migration
type MigrationStatus struct {
	Version    string
	State      string // "Applied", "Pending", "Failed"
	Batch      int
	ExecutedAt *string
}

// CreateTable creates a new database table using the fluent TableBuilder API
func (m *Migrator) CreateTable(name string, fn func(*TableBuilder)) error {
	builder := newTableBuilder(name, m.driver)
	fn(builder)

	sql := builder.ToSQL()
	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to create table %s: %w", name, err)
	}

	return nil
}

// DropTable drops a database table
func (m *Migrator) DropTable(name string) error {
	var sql string

	switch m.driver {
	case "postgres":
		// Postgres needs CASCADE to drop dependent objects
		sql = "DROP TABLE IF EXISTS " + name + " CASCADE"
	default:
		sql = "DROP TABLE IF EXISTS " + name
	}

	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to drop table %s: %w", name, err)
	}

	return nil
}

// Raw executes arbitrary SQL
func (m *Migrator) Raw(sql string) error {
	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to execute raw SQL: %w", err)
	}
	return nil
}

// AddColumn adds a column to an existing table
func (m *Migrator) AddColumn(table, column string, fn func(*ColumnBuilder)) error {
	builder := &ColumnBuilder{
		name:   column,
		driver: m.driver,
	}
	fn(builder)

	sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", table, builder.ToSQL())
	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to add column %s to table %s: %w", column, table, err)
	}
	return nil
}

// DropColumn removes a column from a table
// Note: SQLite does not support DROP COLUMN prior to version 3.35.0
func (m *Migrator) DropColumn(table, column string) error {
	var sql string

	switch m.driver {
	case "postgres":
		sql = fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
	case "mysql":
		sql = fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
	case "sqlite":
		// SQLite 3.35.0+ supports DROP COLUMN
		sql = fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
	default:
		sql = fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", table, column)
	}

	_, err := m.db.Exec(sql)
	if err != nil {
		return fmt.Errorf("failed to drop column %s from table %s: %w", column, table, err)
	}
	return nil
}

// ColumnBuilder provides a fluent API for defining a single column
type ColumnBuilder struct {
	name       string
	driver     string
	colType    string
	length     int
	nullable   bool
	defValue   interface{}
	hasDefault bool
	unique     bool
}

// NewColumnBuilder creates a new ColumnBuilder for the given column name and driver
func NewColumnBuilder(name, driver string) *ColumnBuilder {
	return &ColumnBuilder{
		name:   name,
		driver: driver,
	}
}

// Type sets the column type (string, integer, text, boolean, timestamp, date, biginteger, uuid)
func (c *ColumnBuilder) Type(t string) *ColumnBuilder {
	c.colType = t
	return c
}

// String sets the column type to VARCHAR with optional length (default 255)
func (c *ColumnBuilder) String(length ...int) *ColumnBuilder {
	c.colType = "string"
	c.length = 255
	if len(length) > 0 {
		c.length = length[0]
	}
	return c
}

// Integer sets the column type to INTEGER
func (c *ColumnBuilder) Integer() *ColumnBuilder {
	c.colType = "integer"
	return c
}

// BigInteger sets the column type to BIGINT
func (c *ColumnBuilder) BigInteger() *ColumnBuilder {
	c.colType = "biginteger"
	return c
}

// Text sets the column type to TEXT
func (c *ColumnBuilder) Text() *ColumnBuilder {
	c.colType = "text"
	return c
}

// Boolean sets the column type to BOOLEAN
func (c *ColumnBuilder) Boolean() *ColumnBuilder {
	c.colType = "boolean"
	return c
}

// Timestamp sets the column type to TIMESTAMP
func (c *ColumnBuilder) Timestamp() *ColumnBuilder {
	c.colType = "timestamp"
	return c
}

// Date sets the column type to DATE
func (c *ColumnBuilder) Date() *ColumnBuilder {
	c.colType = "date"
	return c
}

// UUID sets the column type to UUID
func (c *ColumnBuilder) UUID() *ColumnBuilder {
	c.colType = "uuid"
	return c
}

// JSON sets the column type to JSON
func (c *ColumnBuilder) JSON() *ColumnBuilder {
	c.colType = "json"
	return c
}

// JSONB sets the column type to JSONB (binary JSON)
func (c *ColumnBuilder) JSONB() *ColumnBuilder {
	c.colType = "jsonb"
	return c
}

// Nullable marks the column as allowing NULL values
func (c *ColumnBuilder) Nullable() *ColumnBuilder {
	c.nullable = true
	return c
}

// Default sets a default value for the column
func (c *ColumnBuilder) Default(v interface{}) *ColumnBuilder {
	c.defValue = v
	c.hasDefault = true
	return c
}

// Unique marks the column as having a unique constraint
func (c *ColumnBuilder) Unique() *ColumnBuilder {
	c.unique = true
	return c
}

// ToSQL generates the column definition SQL fragment
func (c *ColumnBuilder) ToSQL() string {
	var sql string

	// Column name
	sql = c.name + " "

	// Type mapping based on driver
	switch c.driver {
	case "sqlite":
		sql += c.toSQLiteType()
	case "postgres":
		sql += c.toPostgresType()
	case "mysql":
		sql += c.toMySQLType()
	default:
		sql += c.toSQLiteType()
	}

	// Constraints
	if !c.nullable {
		sql += " NOT NULL"
	}

	if c.unique {
		sql += " UNIQUE"
	}

	if c.hasDefault {
		sql += " DEFAULT " + formatDefaultValue(c.defValue, c.colType, c.driver)
	}

	return sql
}

func (c *ColumnBuilder) toSQLiteType() string {
	switch c.colType {
	case "integer":
		return "INTEGER"
	case "biginteger":
		return "INTEGER"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "boolean":
		return "INTEGER"
	case "timestamp":
		return "DATETIME"
	case "date":
		return "DATE"
	case "uuid":
		return "TEXT"
	case "json", "jsonb":
		return "TEXT"
	default:
		return "TEXT"
	}
}

func (c *ColumnBuilder) toPostgresType() string {
	switch c.colType {
	case "integer":
		return "INTEGER"
	case "biginteger":
		return "BIGINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		return "TIMESTAMP"
	case "date":
		return "DATE"
	case "uuid":
		return "UUID"
	case "json":
		return "JSON"
	case "jsonb":
		return "JSONB"
	default:
		return "TEXT"
	}
}

func (c *ColumnBuilder) toMySQLType() string {
	switch c.colType {
	case "integer":
		return "INT"
	case "biginteger":
		return "BIGINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		return "TIMESTAMP"
	case "date":
		return "DATE"
	case "uuid":
		return "CHAR(36)"
	case "json", "jsonb":
		return "JSON"
	default:
		return "TEXT"
	}
}

// TableBuilder provides a fluent API for defining database tables
type TableBuilder struct {
	tableName           string
	driver              string
	columns             []Column
	lastColumn          *Column   // Track last column for chaining modifiers
	compositePrimaryKey []string  // For composite primary keys
}

// Column represents a table column definition
type Column struct {
	Name          string
	Type          string
	Length        int
	Precision     int // For decimal types
	Scale         int // For decimal types
	Nullable      bool
	Default       interface{}
	Unique        bool
	PrimaryKey    bool
	AutoIncrement bool
}

// newTableBuilder creates a new TableBuilder
func newTableBuilder(tableName, driver string) *TableBuilder {
	return &TableBuilder{
		tableName: tableName,
		driver:    driver,
		columns:   make([]Column, 0),
	}
}

// ID adds an auto-increment primary key column named 'id'
func (t *TableBuilder) ID() *TableBuilder {
	col := Column{
		Name:          "id",
		Type:          "integer",
		PrimaryKey:    true,
		AutoIncrement: true,
		Nullable:      false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// UUIDPrimary adds a UUID primary key column named 'id' with auto-generation
// For PostgreSQL: Uses gen_random_uuid() (built-in since v13) or uuid_generate_v4() (requires pgcrypto)
// For MySQL: Uses UUID() function
// For SQLite: Requires application-level UUID generation
func (t *TableBuilder) UUIDPrimary() *TableBuilder {
	col := Column{
		Name:       "id",
		Type:       "uuid",
		PrimaryKey: true,
		Nullable:   false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// UUID adds a UUID column
func (t *TableBuilder) UUID(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "uuid",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// String adds a VARCHAR column
func (t *TableBuilder) String(name string, length ...int) *TableBuilder {
	colLength := 255
	if len(length) > 0 {
		colLength = length[0]
	}

	col := Column{
		Name:     name,
		Type:     "string",
		Length:   colLength,
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Integer adds an INTEGER column
func (t *TableBuilder) Integer(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "integer",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Boolean adds a BOOLEAN column
func (t *TableBuilder) Boolean(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "boolean",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Text adds a TEXT column (unlimited length)
func (t *TableBuilder) Text(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "text",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Timestamp adds a single TIMESTAMP column
func (t *TableBuilder) Timestamp(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "timestamp",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Date adds a DATE column
func (t *TableBuilder) Date(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "date",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// BigInteger adds a BIGINT column
func (t *TableBuilder) BigInteger(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "biginteger",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// IP creates a column for IP addresses (varchar 45, supports IPv4 and IPv6)
func (t *TableBuilder) IP(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "string",
		Length:   45,
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Decimal creates a numeric column with precision and scale
// e.g., Decimal("price", 10, 2) = numeric(10,2)
func (t *TableBuilder) Decimal(name string, precision, scale int) *TableBuilder {
	col := Column{
		Name:      name,
		Type:      "decimal",
		Precision: precision,
		Scale:     scale,
		Nullable:  false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// JSON adds a JSON column
// For PostgreSQL: JSON type (text-based, validates JSON on insert)
// For MySQL: JSON type (binary storage)
// For SQLite: TEXT (no native JSON type)
func (t *TableBuilder) JSON(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "json",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// JSONB adds a JSONB column (binary JSON)
// For PostgreSQL: JSONB type (binary storage, indexable, faster queries)
// For MySQL: JSON type (MySQL has no separate JSONB)
// For SQLite: TEXT (no native JSON type)
func (t *TableBuilder) JSONB(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "jsonb",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Timestamps adds created_at and updated_at columns
func (t *TableBuilder) Timestamps() *TableBuilder {
	createdAt := Column{
		Name:     "created_at",
		Type:     "timestamp",
		Nullable: false,
	}
	updatedAt := Column{
		Name:     "updated_at",
		Type:     "timestamp",
		Nullable: false,
	}
	t.columns = append(t.columns, createdAt, updatedAt)
	// Don't set lastColumn for Timestamps since it adds multiple columns
	return t
}

// SoftDeletes adds a deleted_at column for soft delete support
func (t *TableBuilder) SoftDeletes() *TableBuilder {
	col := Column{
		Name:     "deleted_at",
		Type:     "timestamp",
		Nullable: true,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Unique marks the previous column as unique
func (t *TableBuilder) Unique() *TableBuilder {
	if t.lastColumn != nil {
		t.lastColumn.Unique = true
	}
	return t
}

// Nullable allows NULL values for the previous column
func (t *TableBuilder) Nullable() *TableBuilder {
	if t.lastColumn != nil {
		t.lastColumn.Nullable = true
	}
	return t
}

// Default sets a default value for the previous column
func (t *TableBuilder) Default(value interface{}) *TableBuilder {
	if t.lastColumn != nil {
		t.lastColumn.Default = value
	}
	return t
}

// Primary makes the current column a primary key (for 1:1 relations where FK is the PK)
func (t *TableBuilder) Primary() *TableBuilder {
	if t.lastColumn != nil {
		t.lastColumn.PrimaryKey = true
	}
	return t
}

// PrimaryKey sets a composite primary key (for junction tables without auto-increment ID)
func (t *TableBuilder) PrimaryKey(columns ...string) *TableBuilder {
	t.compositePrimaryKey = columns
	return t
}

// ToSQL generates driver-specific CREATE TABLE SQL
func (t *TableBuilder) ToSQL() string {
	var sql string

	switch t.driver {
	case "sqlite":
		sql = t.toSQLiteSyntax()
	case "postgres":
		sql = t.toPostgresSyntax()
	case "mysql":
		sql = t.toMySQLSyntax()
	default:
		sql = t.toSQLiteSyntax() // Default to SQLite
	}

	return sql
}

func (t *TableBuilder) toSQLiteSyntax() string {
	sql := "CREATE TABLE " + t.tableName + " (\n"

	for i, col := range t.columns {
		sql += "  " + col.Name + " "

		// Type mapping
		switch col.Type {
		case "integer":
			if col.PrimaryKey && col.AutoIncrement {
				sql += "INTEGER PRIMARY KEY AUTOINCREMENT"
			} else {
				sql += "INTEGER"
				if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
					sql += " PRIMARY KEY"
				}
			}
		case "biginteger":
			sql += "INTEGER" // SQLite uses INTEGER for all int sizes
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "boolean":
			sql += "INTEGER" // SQLite uses 0/1 for boolean
		case "timestamp":
			sql += "DATETIME"
			if !col.Nullable {
				sql += " DEFAULT CURRENT_TIMESTAMP"
			}
		case "date":
			sql += "DATE"
		case "uuid":
			sql += "TEXT" // SQLite doesn't have native UUID, use TEXT
			if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
				sql += " PRIMARY KEY"
			}
		case "decimal":
			sql += fmt.Sprintf("NUMERIC(%d,%d)", col.Precision, col.Scale)
		case "json", "jsonb":
			sql += "TEXT" // SQLite has no native JSON type
		}

		// Constraints
		if !col.PrimaryKey && !col.Nullable {
			sql += " NOT NULL"
		}

		if col.Unique && !col.PrimaryKey {
			sql += " UNIQUE"
		}

		if col.Default != nil && col.Type != "timestamp" {
			sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "sqlite")
		}

		if i < len(t.columns)-1 || len(t.compositePrimaryKey) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Add composite primary key constraint
	if len(t.compositePrimaryKey) > 0 {
		sql += "  PRIMARY KEY ("
		for i, col := range t.compositePrimaryKey {
			if i > 0 {
				sql += ", "
			}
			sql += col
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toPostgresSyntax() string {
	sql := "CREATE TABLE " + t.tableName + " (\n"

	for i, col := range t.columns {
		sql += "  " + col.Name + " "

		// Type mapping
		switch col.Type {
		case "integer":
			if col.PrimaryKey && col.AutoIncrement {
				sql += "SERIAL PRIMARY KEY"
			} else {
				sql += "INTEGER"
				if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
					sql += " PRIMARY KEY"
				}
			}
		case "biginteger":
			sql += "BIGINT"
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "boolean":
			sql += "BOOLEAN"
		case "timestamp":
			sql += "TIMESTAMP"
			if !col.Nullable {
				sql += " DEFAULT NOW()"
			}
		case "date":
			sql += "DATE"
		case "uuid":
			if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
				sql += "UUID PRIMARY KEY DEFAULT gen_random_uuid()"
			} else {
				sql += "UUID"
			}
		case "decimal":
			sql += fmt.Sprintf("NUMERIC(%d,%d)", col.Precision, col.Scale)
		case "json":
			sql += "JSON"
		case "jsonb":
			sql += "JSONB"
		}

		// Constraints (skip if already handled by SERIAL PRIMARY KEY or UUID PRIMARY KEY)
		if !(col.PrimaryKey && col.AutoIncrement) && !(col.PrimaryKey && col.Type == "uuid" && len(t.compositePrimaryKey) == 0) {
			if !col.Nullable {
				sql += " NOT NULL"
			}

			if col.Unique {
				sql += " UNIQUE"
			}

			if col.Default != nil && col.Type != "timestamp" {
				sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "postgres")
			}
		}

		if i < len(t.columns)-1 || len(t.compositePrimaryKey) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Add composite primary key constraint
	if len(t.compositePrimaryKey) > 0 {
		sql += "  PRIMARY KEY ("
		for i, col := range t.compositePrimaryKey {
			if i > 0 {
				sql += ", "
			}
			sql += col
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toMySQLSyntax() string {
	sql := "CREATE TABLE " + t.tableName + " (\n"

	for i, col := range t.columns {
		sql += "  " + col.Name + " "

		// Type mapping
		switch col.Type {
		case "integer":
			if col.PrimaryKey && col.AutoIncrement {
				sql += "INT AUTO_INCREMENT PRIMARY KEY"
			} else {
				sql += "INT"
				if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
					sql += " PRIMARY KEY"
				}
			}
		case "biginteger":
			sql += "BIGINT"
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "boolean":
			sql += "BOOLEAN"
		case "timestamp":
			sql += "TIMESTAMP"
			if !col.Nullable {
				sql += " DEFAULT CURRENT_TIMESTAMP"
			}
		case "date":
			sql += "DATE"
		case "uuid":
			if col.PrimaryKey && len(t.compositePrimaryKey) == 0 {
				sql += "CHAR(36) PRIMARY KEY"
			} else {
				sql += "CHAR(36)"
			}
		case "decimal":
			sql += fmt.Sprintf("DECIMAL(%d,%d)", col.Precision, col.Scale)
		case "json", "jsonb":
			sql += "JSON" // MySQL has no separate JSONB type
		}

		// Constraints (skip if already handled by AUTO_INCREMENT PRIMARY KEY or UUID PRIMARY KEY)
		if !(col.PrimaryKey && col.AutoIncrement) && !(col.PrimaryKey && col.Type == "uuid" && len(t.compositePrimaryKey) == 0) {
			if !col.Nullable {
				sql += " NOT NULL"
			}

			if col.Unique {
				sql += " UNIQUE"
			}

			if col.Default != nil && col.Type != "timestamp" {
				sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "mysql")
			}
		}

		if i < len(t.columns)-1 || len(t.compositePrimaryKey) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Add composite primary key constraint
	if len(t.compositePrimaryKey) > 0 {
		sql += "  PRIMARY KEY ("
		for i, col := range t.compositePrimaryKey {
			if i > 0 {
				sql += ", "
			}
			sql += col
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

func formatDefaultValue(value interface{}, colType string, driver string) string {
	switch v := value.(type) {
	case string:
		return "'" + v + "'"
	case int, int64, int32:
		return fmt.Sprintf("%d", v)
	case bool:
		// PostgreSQL BOOLEAN type requires true/false literals, not 0/1
		if driver == "postgres" && colType == "boolean" {
			if v {
				return "true"
			}
			return "false"
		}
		// SQLite and MySQL can use 0/1
		if v {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}
