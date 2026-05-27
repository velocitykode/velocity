package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// migrationLockKey is a fixed 64-bit integer used with pg_advisory_lock.
// Any constant works as long as it is stable across runners; this value
// is the FNV-1a hash of "velocity.migrate.lock" (precomputed) and is
// highly unlikely to collide with user-defined advisory lock keys.
const migrationLockKey int64 = 0x76656c6d69677261 // "velmigra" as bytes

// migrationsLockTableName is the name of the helper row used for MySQL and
// SQLite advisory locking. The row-level SELECT ... FOR UPDATE blocks
// concurrent runners until the current transaction resolves.
const migrationsLockTableName = "migrations_lock"

// Migrator handles migration execution for a specific database connection
type Migrator struct {
	db             *sql.DB
	driver         string
	migrationsPath string
	pretend        bool
	pretendLog     []string

	// conn is an optional pinned *sql.Conn used to route all migration
	// SQL through a single backend connection. This is required for
	// drivers whose advisory-lock primitives are session-scoped (most
	// notably Postgres's pg_advisory_lock / pg_advisory_unlock pair),
	// where acquiring and releasing the lock on different pooled
	// connections would silently leak the lock. See acquireMigrationLock
	// for how this field is populated for the Postgres path.
	//
	// When nil, helpers fall back to *sql.DB which lets database/sql
	// pick any connection from the pool, which is fine for drivers
	// whose locks are not session-scoped (MySQL row-lock tx, SQLite CAS row).
	conn *sql.Conn

	// lockMu serializes lock acquire/release on this Migrator instance and
	// guards lockDepth + lockRelease. The migration lock itself is held by
	// the driver-specific primitive (pg_advisory_lock, MySQL FOR UPDATE tx,
	// SQLite CAS row); lockMu only coordinates *this* instance's view of
	// whether it already holds that lock.
	lockMu sync.Mutex

	// lockDepth tracks how many nested entry points on THIS Migrator
	// instance currently hold the migration lock. The outermost caller
	// acquires the driver primitive at depth 1; nested DDL helpers invoked
	// from within a migration body increment the counter and skip the
	// acquire (and decrement on return), letting them pass through without
	// deadlocking. Lock is fully released when the counter returns to 0.
	lockDepth int

	// lockRelease is the driver-specific release function captured at the
	// outermost acquire; only invoked when lockDepth drops back to 0.
	lockRelease func()
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
// SetPretend enables pretend mode — SQL is collected instead of executed.
func (m *Migrator) SetPretend(pretend bool) {
	m.pretend = pretend
	m.pretendLog = nil
}

// PretendLog returns the collected SQL statements from pretend mode.
func (m *Migrator) PretendLog() []string {
	return m.pretendLog
}

// Driver returns the database driver name ("postgres", "mysql", "sqlite")
// that this Migrator was constructed with. Migrations can use this to
// pick driver-specific DDL via a Raw() call.
func (m *Migrator) Driver() string {
	return m.driver
}

// exec runs SQL or collects it in pretend mode.
func (m *Migrator) exec(sql string) error {
	if m.pretend {
		m.pretendLog = append(m.pretendLog, sql)
		return nil
	}
	_, err := m.execContext(context.Background(), sql)
	return err
}

// execContext routes an Exec through the pinned *sql.Conn when present,
// falling back to the pooled *sql.DB. All migration SQL that may run
// while a session-scoped lock is held must go through this helper so
// the lock and the work share the same backend connection.
func (m *Migrator) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if m.conn != nil {
		return m.conn.ExecContext(ctx, query, args...)
	}
	return m.db.ExecContext(ctx, query, args...)
}

// queryContext mirrors execContext for SELECT statements that return rows.
func (m *Migrator) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if m.conn != nil {
		return m.conn.QueryContext(ctx, query, args...)
	}
	return m.db.QueryContext(ctx, query, args...)
}

// queryRowContext mirrors execContext for single-row SELECT statements.
func (m *Migrator) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if m.conn != nil {
		return m.conn.QueryRowContext(ctx, query, args...)
	}
	return m.db.QueryRowContext(ctx, query, args...)
}

func (m *Migrator) SetMigrationsPath(path string) {
	m.migrationsPath = path
}

// Up runs all pending migrations under a database-level advisory lock so
// concurrent migrator processes cannot double-apply a migration.
//
// Locking strategy:
//   - Postgres: pg_advisory_lock(migrationLockKey), session-scoped lock
//     released via pg_advisory_unlock when Up returns.
//   - MySQL/SQLite: a dedicated single-row "migrations_lock" table is
//     created on demand and acquired via SELECT ... FOR UPDATE inside a
//     transaction that is held for the duration of Up. Releasing is as
//     simple as rolling back (on error) or committing (on success) the
//     lock transaction. SQLite serializes writes at the database level;
//     acquiring the row lock is effectively equivalent.
//
// When the lock cannot be acquired (e.g. network partition), Up returns
// the underlying error. When it can be acquired but has already been
// taken by another runner, the call blocks until the holder releases.
func (m *Migrator) Up() error {
	return m.withMigrationLock(m.runUp)
}

// withMigrationLock executes fn while holding the migration lock for this
// Migrator instance. The lock is re-entrant on a single Migrator: nested
// invocations (e.g. DDL helpers called from inside a migration body) see
// lockDepth > 0 and skip both acquire and release, passing fn straight
// through. The outermost caller acquires the driver primitive, runs fn,
// then releases when lockDepth returns to 0.
//
// Pretend mode skips locking entirely: no database mutation occurs so
// there is nothing to serialize. This matches the previous Up()-only
// pretend-mode semantics now that every schema-mutating entry point
// flows through this helper.
//
// Re-entrance is keyed on the Migrator instance, not the underlying
// *sql.DB. Two different Migrator structs against the same database
// still contend on the driver-level lock, which is what makes
// cross-process serialization work. This intentional asymmetry is what
// lets Fresh() hold a single outer lock across its drop-then-Up pipeline
// while Up()'s inner call no-ops on the same instance.
func (m *Migrator) withMigrationLock(fn func() error) error {
	if m.pretend {
		// Pretend mode is pure SQL collection: no database mutation
		// and therefore no locking required.
		return fn()
	}

	m.lockMu.Lock()
	if m.lockDepth > 0 {
		m.lockDepth++
		m.lockMu.Unlock()
		defer func() {
			m.lockMu.Lock()
			m.lockDepth--
			m.lockMu.Unlock()
		}()
		return fn()
	}

	// Outermost acquire. We hold lockMu while taking the driver
	// primitive so a concurrent re-entrant caller on this instance does
	// not race past us and treat the half-acquired state as held.
	release, err := m.acquireMigrationLock()
	if err != nil {
		m.lockMu.Unlock()
		return fmt.Errorf("velocity/orm: failed to acquire migration lock: %w", err)
	}
	m.lockDepth = 1
	m.lockRelease = release
	m.lockMu.Unlock()

	defer func() {
		m.lockMu.Lock()
		m.lockDepth--
		if m.lockDepth == 0 {
			r := m.lockRelease
			m.lockRelease = nil
			m.lockMu.Unlock()
			if r != nil {
				r()
			}
			return
		}
		m.lockMu.Unlock()
	}()

	return fn()
}

// runUp is the migration-execution body; acquireMigrationLock guarantees
// exclusivity before it is invoked.
func (m *Migrator) runUp() error {
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

// acquireMigrationLock takes the migration lock appropriate for the
// active driver. The returned function releases the lock and must be
// deferred immediately after a successful acquisition.
//
// The strategies below differ by engine:
//   - Postgres: session-scoped pg_advisory_lock. Blocks the caller
//     until the lock is free; released via pg_advisory_unlock.
//   - MySQL: SELECT ... FOR UPDATE on a dedicated single-row table
//     inside a long-lived transaction that gates all migration work.
//     MySQL's row-lock blocks siblings until the tx commits or rolls
//     back.
//   - SQLite: an atomic compare-and-set UPDATE on a lock row plus a
//     bounded busy-wait loop. We deliberately avoid holding an open
//     transaction because SQLite's single-writer model would deadlock
//     the migration body (which opens its own connection) against the
//     lock transaction.
func (m *Migrator) acquireMigrationLock() (release func(), err error) {
	switch m.driver {
	case "postgres":
		// pg_advisory_lock / pg_advisory_unlock are SESSION-scoped: the
		// unlock must run on the same backend connection that took the
		// lock, otherwise it is a silent no-op and the lock leaks until
		// the holding session terminates. *sql.DB is a connection pool
		// with no affinity between successive Exec calls, so we must
		// pin a dedicated *sql.Conn for the duration of the migration
		// run and route every subsequent query through it.
		ctx := context.Background()
		conn, connErr := m.db.Conn(ctx)
		if connErr != nil {
			return nil, fmt.Errorf("velocity/orm: pin migration conn: %w", connErr)
		}
		if _, lockErr := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); lockErr != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("velocity/orm: pg_advisory_lock: %w", lockErr)
		}
		m.conn = conn
		return func() {
			// Release on the SAME conn that took the lock. Postgres
			// also drops session locks when the conn closes, so the
			// Close below is a defensive backstop if Exec fails.
			_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
			m.conn = nil
			_ = conn.Close()
		}, nil

	case "mysql":
		if err := m.ensureLockTable(); err != nil {
			return nil, err
		}
		tx, err := m.db.Begin()
		if err != nil {
			return nil, fmt.Errorf("velocity/orm: begin lock tx: %w", err)
		}
		if _, err := tx.Exec(
			"INSERT IGNORE INTO " + quoteIdentifier(migrationsLockTableName, m.driver) + " (id, locked) VALUES (1, 0)",
		); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("velocity/orm: seed lock row: %w", err)
		}
		if _, err := tx.Exec(
			"SELECT id FROM " + quoteIdentifier(migrationsLockTableName, m.driver) + " WHERE id = 1 FOR UPDATE",
		); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("velocity/orm: select for update lock: %w", err)
		}
		return func() {
			// Commit releases the row lock. Rollback would also work,
			// but Commit is a cheap no-op for a SELECT-only tx.
			_ = tx.Commit()
		}, nil

	case "sqlite":
		if err := m.ensureLockTable(); err != nil {
			return nil, err
		}
		if err := m.seedLockRow(); err != nil {
			return nil, err
		}
		if err := m.sqliteAcquireLock(); err != nil {
			return nil, err
		}
		return func() {
			_, _ = m.db.Exec(
				"UPDATE " + quoteIdentifier(migrationsLockTableName, m.driver) + " SET locked = 0 WHERE id = 1",
			)
		}, nil

	default:
		// Unknown drivers silently skip locking — they have no storage
		// to contend over in practice. Return a no-op releaser.
		return func() {}, nil
	}
}

// seedLockRow inserts the single-row lock record if it does not already
// exist. Safe to call concurrently: the SELECT guard keeps the INSERT
// idempotent even when two callers race.
func (m *Migrator) seedLockRow() error {
	_, err := m.db.Exec(
		"INSERT INTO " + quoteIdentifier(migrationsLockTableName, m.driver) +
			" (id, locked) SELECT 1, 0 WHERE NOT EXISTS (SELECT 1 FROM " +
			quoteIdentifier(migrationsLockTableName, m.driver) + " WHERE id = 1)",
	)
	if err != nil {
		return fmt.Errorf("velocity/orm: seed lock row: %w", err)
	}
	return nil
}

// sqliteAcquireLock performs a bounded compare-and-set spin to acquire
// the migration lock. The UPDATE is atomic at the row level in SQLite,
// so the caller whose UPDATE affects 1 row owns the lock; all others
// see 0 and retry after a short sleep.
//
// Timeout is generous (30s) to accommodate long-running migrations
// without pathological lockups.
func (m *Migrator) sqliteAcquireLock() error {
	const (
		attemptCap    = 600
		backoffMs     = 50
		timeoutErrFmt = "velocity/orm: sqlite migration lock timeout after %d attempts"
	)
	for i := 0; i < attemptCap; i++ {
		res, err := m.db.Exec(
			"UPDATE " + quoteIdentifier(migrationsLockTableName, m.driver) +
				" SET locked = 1 WHERE id = 1 AND locked = 0",
		)
		if err != nil {
			return fmt.Errorf("velocity/orm: acquire lock row: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("velocity/orm: rows affected: %w", err)
		}
		if rows == 1 {
			return nil
		}
		time.Sleep(backoffMs * time.Millisecond)
	}
	return fmt.Errorf(timeoutErrFmt, attemptCap)
}

// ensureLockTable creates the single-row table used by the MySQL and
// SQLite advisory-lock strategies. Safe to call concurrently.
func (m *Migrator) ensureLockTable() error {
	var createSQL string
	switch m.driver {
	case "mysql":
		createSQL = "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(migrationsLockTableName, m.driver) + " (id INT PRIMARY KEY, locked TINYINT NOT NULL DEFAULT 0) ENGINE=InnoDB"
	case "sqlite":
		createSQL = "CREATE TABLE IF NOT EXISTS " + quoteIdentifier(migrationsLockTableName, m.driver) + " (id INTEGER PRIMARY KEY, locked INTEGER NOT NULL DEFAULT 0)"
	default:
		return nil
	}
	if _, err := m.db.Exec(createSQL); err != nil {
		return fmt.Errorf("velocity/orm: ensure lock table: %w", err)
	}
	return nil
}

// Down rolls back the last N batches of migrations under the same
// migration lock used by Up. Without the lock, a concurrent migrator
// process could run Up while this one ran Down, leaving the schema in
// an unrecoverable partial state.
func (m *Migrator) Down(steps int) error {
	return m.withMigrationLock(func() error {
		return m.runDown(steps)
	})
}

// runDown is the rollback body; withMigrationLock guarantees exclusivity
// before it is invoked.
func (m *Migrator) runDown(steps int) error {
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

// Fresh drops all tables and re-runs all migrations. The lock is held
// across BOTH the drop pass and the subsequent Up so a concurrent
// migrator process cannot start applying migrations to tables that this
// process is in the middle of dropping (which would corrupt the schema
// into an unrecoverable intermediate state). The nested Up() call sees
// lockDepth > 0 and skips its own acquire via withMigrationLock's
// re-entrance.
func (m *Migrator) Fresh() error {
	return m.withMigrationLock(func() error {
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

		// Run all migrations under the same outer lock.
		return m.Up()
	})
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

// CreateTable creates a new database table using the fluent TableBuilder API.
// Input is validated before the lock is taken so malformed calls fail fast
// without spending a lock acquisition. The DDL runs under the migration
// lock so standalone callers (outside a migration body) do not race a
// concurrent Up/Down/Fresh. When invoked from within a migration body the
// call is re-entrant on the same Migrator instance and passes through
// without re-acquiring.
func (m *Migrator) CreateTable(name string, fn func(*TableBuilder)) error {
	builder := newTableBuilder(name, m.driver)
	fn(builder)

	for _, col := range builder.columns {
		if !ddlIdentifierRegex.MatchString(col.Name) {
			return fmt.Errorf("invalid column name: %q", col.Name)
		}
	}
	for _, col := range builder.compositePrimaryKey {
		if !ddlIdentifierRegex.MatchString(col) {
			return fmt.Errorf("invalid primary key column name: %q", col)
		}
	}

	sql := builder.ToSQL()
	return m.withMigrationLock(func() error {
		if err := m.exec(sql); err != nil {
			return fmt.Errorf("failed to create table %s: %w", name, err)
		}
		return nil
	})
}

// DropTable drops a database table. Held under the migration lock so
// standalone callers (outside a migration body) do not race a concurrent
// Up/Down/Fresh. Re-entrant within a migration body.
func (m *Migrator) DropTable(name string) error {
	return m.withMigrationLock(func() error {
		quoted := quoteIdentifier(name, m.driver)
		var sql string

		switch m.driver {
		case "postgres":
			// Postgres needs CASCADE to drop dependent objects
			sql = "DROP TABLE IF EXISTS " + quoted + " CASCADE"
		default:
			sql = "DROP TABLE IF EXISTS " + quoted
		}

		if err := m.exec(sql); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", name, err)
		}

		return nil
	})
}

// Raw executes arbitrary SQL.
//
// WARNING: This method executes raw SQL directly. The caller is responsible for
// preventing SQL injection by using parameterized queries with placeholder arguments.
// Never concatenate user input directly into the sql string.
//
// Held under the migration lock so standalone callers do not race a
// concurrent Up/Down/Fresh. Re-entrant within a migration body.
func (m *Migrator) Raw(sql string) error {
	return m.withMigrationLock(func() error {
		if err := m.exec(sql); err != nil {
			return fmt.Errorf("failed to execute raw SQL: %w", err)
		}
		return nil
	})
}

// Table modifies an existing table using the same TableBuilder API as CreateTable.
// Each column added via the builder generates an ALTER TABLE ADD COLUMN statement.
// Primary key columns (ID, UUIDPrimary) are rejected since they cannot be added to existing tables.
//
// Input is validated before the lock is taken so malformed calls fail
// fast. The DDL runs under the migration lock so standalone callers do
// not race a concurrent Up/Down/Fresh. Re-entrant within a migration body.
func (m *Migrator) Table(name string, fn func(*TableBuilder)) error {
	builder := newTableBuilder(name, m.driver)
	fn(builder)

	type stmt struct {
		sql string
	}
	stmts := make([]stmt, 0, len(builder.columns))
	for _, col := range builder.columns {
		if col.PrimaryKey {
			return fmt.Errorf("cannot add primary key column %q to existing table %q via ALTER TABLE", col.Name, name)
		}
		if !ddlIdentifierRegex.MatchString(col.Name) {
			return fmt.Errorf("invalid column name: %q", col.Name)
		}
		colSQL := columnToSQL(col, m.driver)
		stmts = append(stmts, stmt{
			sql: fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quoteIdentifier(name, m.driver), colSQL),
		})
	}

	return m.withMigrationLock(func() error {
		for _, s := range stmts {
			if err := m.exec(s.sql); err != nil {
				return fmt.Errorf("failed to alter table %s: %w", name, err)
			}
		}
		return nil
	})
}

// AddColumn adds a column to an existing table. Input is validated
// before the lock is taken so malformed calls fail fast. The DDL runs
// under the migration lock so standalone callers do not race a concurrent
// Up/Down/Fresh. Re-entrant within a migration body.
func (m *Migrator) AddColumn(table, column string, fn func(*ColumnBuilder)) error {
	if !ddlIdentifierRegex.MatchString(column) {
		return fmt.Errorf("invalid column name: %q", column)
	}
	builder := &ColumnBuilder{
		name:   column,
		driver: m.driver,
	}
	fn(builder)

	sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quoteIdentifier(table, m.driver), builder.ToSQL())
	return m.withMigrationLock(func() error {
		if err := m.exec(sql); err != nil {
			return fmt.Errorf("failed to add column %s to table %s: %w", column, table, err)
		}
		return nil
	})
}

// DropColumn removes a column from a table. Held under the migration
// lock so standalone callers do not race a concurrent Up/Down/Fresh.
// Re-entrant within a migration body.
// Note: SQLite does not support DROP COLUMN prior to version 3.35.0
func (m *Migrator) DropColumn(table, column string) error {
	return m.withMigrationLock(func() error {
		quotedTable := quoteIdentifier(table, m.driver)
		quotedColumn := quoteIdentifier(column, m.driver)
		sql := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", quotedTable, quotedColumn)

		if err := m.exec(sql); err != nil {
			return fmt.Errorf("failed to drop column %s from table %s: %w", column, table, err)
		}
		return nil
	})
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
	sql = quoteIdentifier(c.name, c.driver) + " "

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
	lastColumn          *Column  // Track last column for chaining modifiers
	compositePrimaryKey []string // For composite primary keys
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
	sql := "CREATE TABLE " + quoteIdentifier(t.tableName, t.driver) + " (\n"

	for i, col := range t.columns {
		sql += "  " + quoteIdentifier(col.Name, t.driver) + " "

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
			sql += quoteIdentifier(col, t.driver)
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toPostgresSyntax() string {
	sql := "CREATE TABLE " + quoteIdentifier(t.tableName, t.driver) + " (\n"

	for i, col := range t.columns {
		sql += "  " + quoteIdentifier(col.Name, t.driver) + " "

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
			sql += quoteIdentifier(col, t.driver)
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toMySQLSyntax() string {
	sql := "CREATE TABLE " + quoteIdentifier(t.tableName, t.driver) + " (\n"

	for i, col := range t.columns {
		sql += "  " + quoteIdentifier(col.Name, t.driver) + " "

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
			sql += quoteIdentifier(col, t.driver)
		}
		sql += ")\n"
	}

	sql += ")"
	return sql
}

// columnToSQL generates a driver-aware column definition SQL fragment from a Column struct.
// Used by Table() to produce ALTER TABLE ADD COLUMN statements.
// Column names are validated by the caller (Table()) before reaching this function.
func columnToSQL(col Column, driver string) string {
	var sql string
	sql = quoteIdentifier(col.Name, driver) + " "

	switch driver {
	case "postgres":
		sql += columnTypePostgres(col)
	case "mysql":
		sql += columnTypeMySQL(col)
	default: // sqlite
		sql += columnTypeSQLite(col)
	}

	// Skip constraints if already embedded in the type string (e.g. SERIAL PRIMARY KEY)
	if col.PrimaryKey && col.AutoIncrement {
		return sql
	}
	if col.PrimaryKey && col.Type == "uuid" {
		return sql
	}

	if !col.Nullable {
		sql += " NOT NULL"
	}
	if col.Unique && !col.PrimaryKey {
		sql += " UNIQUE"
	}
	if col.Default != nil && col.Type != "timestamp" {
		sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, driver)
	}

	return sql
}

func columnTypeSQLite(col Column) string {
	switch col.Type {
	case "integer":
		if col.PrimaryKey && col.AutoIncrement {
			return "INTEGER PRIMARY KEY AUTOINCREMENT"
		}
		return "INTEGER"
	case "biginteger":
		return "INTEGER"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "boolean":
		return "INTEGER"
	case "timestamp":
		s := "DATETIME"
		if !col.Nullable {
			s += " DEFAULT CURRENT_TIMESTAMP"
		}
		return s
	case "date":
		return "DATE"
	case "uuid":
		if col.PrimaryKey {
			return "TEXT PRIMARY KEY"
		}
		return "TEXT"
	case "decimal":
		return fmt.Sprintf("NUMERIC(%d,%d)", col.Precision, col.Scale)
	case "json", "jsonb":
		return "TEXT"
	default:
		return "TEXT"
	}
}

func columnTypePostgres(col Column) string {
	switch col.Type {
	case "integer":
		if col.PrimaryKey && col.AutoIncrement {
			return "SERIAL PRIMARY KEY"
		}
		return "INTEGER"
	case "biginteger":
		return "BIGINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		s := "TIMESTAMP"
		if !col.Nullable {
			s += " DEFAULT NOW()"
		}
		return s
	case "date":
		return "DATE"
	case "uuid":
		if col.PrimaryKey {
			return "UUID PRIMARY KEY DEFAULT gen_random_uuid()"
		}
		return "UUID"
	case "decimal":
		return fmt.Sprintf("NUMERIC(%d,%d)", col.Precision, col.Scale)
	case "json":
		return "JSON"
	case "jsonb":
		return "JSONB"
	default:
		return "TEXT"
	}
}

func columnTypeMySQL(col Column) string {
	switch col.Type {
	case "integer":
		if col.PrimaryKey && col.AutoIncrement {
			return "INT AUTO_INCREMENT PRIMARY KEY"
		}
		return "INT"
	case "biginteger":
		return "BIGINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		s := "TIMESTAMP"
		if !col.Nullable {
			s += " DEFAULT CURRENT_TIMESTAMP"
		}
		return s
	case "date":
		return "DATE"
	case "uuid":
		if col.PrimaryKey {
			return "CHAR(36) PRIMARY KEY"
		}
		return "CHAR(36)"
	case "decimal":
		return fmt.Sprintf("DECIMAL(%d,%d)", col.Precision, col.Scale)
	case "json", "jsonb":
		return "JSON"
	default:
		return "TEXT"
	}
}

func formatDefaultValue(value interface{}, colType string, driver string) string {
	switch v := value.(type) {
	case string:
		return "'" + strings.ReplaceAll(v, "'", "''") + "'"
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

// ddlIdentifierRegex validates SQL identifiers for DDL statements
var ddlIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// quoteIdentifier quotes a database identifier for DDL statements based on driver
func quoteIdentifier(name, driver string) string {
	switch driver {
	case "mysql", "sqlite":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	case "postgres":
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	default:
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}
