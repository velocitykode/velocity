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
		if err := validateVectorColumn(col, m.driver); err != nil {
			return err
		}
	}
	for _, col := range builder.compositePrimaryKey {
		if !ddlIdentifierRegex.MatchString(col) {
			return fmt.Errorf("invalid primary key column name: %q", col)
		}
	}
	for _, ck := range builder.checks {
		if !ddlIdentifierRegex.MatchString(ck.name) {
			return fmt.Errorf("invalid check constraint name: %q", ck.name)
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

// CreateVectorExtension ensures the pgvector extension is installed
// (CREATE EXTENSION IF NOT EXISTS vector). Call it before creating vector
// columns or indexes. PostgreSQL only: on any other driver it returns an error
// rather than emitting DDL the dialect cannot run. Honors pretend mode and the
// migration lock (it routes through Raw).
func (m *Migrator) CreateVectorExtension() error {
	if m.driver != "postgres" {
		return fmt.Errorf("vector extension is only supported on postgres (driver %q)", m.driver)
	}
	return m.Raw("CREATE EXTENSION IF NOT EXISTS vector")
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

	colStmts := make([]string, 0, len(builder.columns))
	for _, col := range builder.columns {
		if col.PrimaryKey {
			return fmt.Errorf("cannot add primary key column %q to existing table %q via ALTER TABLE", col.Name, name)
		}
		if !ddlIdentifierRegex.MatchString(col.Name) {
			return fmt.Errorf("invalid column name: %q", col.Name)
		}
		if err := validateVectorColumn(col, m.driver); err != nil {
			return err
		}
		colStmts = append(colStmts, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quoteIdentifier(name, m.driver), columnToSQL(col, m.driver)))
	}

	// Named CHECK constraints. Postgres/MySQL add them via ALTER TABLE ...
	// ADD CONSTRAINT. SQLite has no ADD CONSTRAINT, so its checks are applied
	// by rebuilding the table (sqliteRebuildWithChecks), which also runs the
	// column adds inside the same transaction so the whole Table() call is
	// atomic and on a single connection.
	checkStmts := make([]string, 0, len(builder.checks))
	for _, ck := range builder.checks {
		if !ddlIdentifierRegex.MatchString(ck.name) {
			return fmt.Errorf("invalid check constraint name: %q", ck.name)
		}
		if m.driver != "sqlite" {
			checkStmts = append(checkStmts, fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s CHECK (%s)", quoteIdentifier(name, m.driver), quoteIdentifier(ck.name, m.driver), ck.expr))
		}
	}

	return m.withMigrationLock(func() error {
		if m.driver == "sqlite" && len(builder.checks) > 0 {
			if err := m.sqliteRebuildWithChecks(name, colStmts, builder.checks); err != nil {
				return fmt.Errorf("failed to alter table %s: %w", name, err)
			}
			return nil
		}
		for _, s := range colStmts {
			if err := m.exec(s); err != nil {
				return fmt.Errorf("failed to alter table %s: %w", name, err)
			}
		}
		for _, s := range checkStmts {
			if err := m.exec(s); err != nil {
				return fmt.Errorf("failed to alter table %s: %w", name, err)
			}
		}
		return nil
	})
}

// sqliteRebuildWithChecks applies a Table() call that adds CHECK constraints on
// SQLite, which has no ALTER TABLE ADD CONSTRAINT. It follows SQLite's
// documented "other kinds of table schema changes" procedure
// (https://sqlite.org/lang_altertable.html): run any pending ALTER TABLE ADD
// COLUMN statements, build a new table whose schema is the (post-add) original
// plus the new constraints, copy the rows across, drop the original, rename the
// new table into its place, then recreate the original's explicit indexes and
// triggers.
//
// The ENTIRE operation (column adds + rebuild) runs on a single pinned
// connection inside ONE transaction with foreign keys disabled (the manual's
// requirement). This makes the whole Table() call atomic: if the new
// constraint is violated by existing rows, the transaction rolls back and the
// column adds are undone too. The connection's prior foreign_keys setting is
// restored before it returns to the pool.
//
// The rebuilt schema is derived from the original's stored CREATE TABLE text in
// sqlite_master: everything between the first '(' and the last ')' (the column
// and constraint list) is reused verbatim, so existing columns, defaults, and
// constraints are preserved exactly; any trailing table options (WITHOUT
// ROWID, STRICT) are carried over too. This relies on the table name being a
// plain identifier (no embedded parentheses), which the framework guarantees
// for tables it creates.
func (m *Migrator) sqliteRebuildWithChecks(name string, preStmts []string, checks []checkConstraint) error {
	ctx := context.Background()

	if m.pretend {
		m.pretendLog = append(m.pretendLog, preStmts...)
		for _, ck := range checks {
			m.pretendLog = append(m.pretendLog,
				fmt.Sprintf("-- sqlite: rebuild %s to add CONSTRAINT %s CHECK (%s)",
					quoteIdentifier(name, m.driver), quoteIdentifier(ck.name, m.driver), ck.expr))
		}
		return nil
	}

	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin sqlite conn: %w", err)
	}
	defer conn.Close()

	// foreign_keys is per-connection and cannot be toggled inside a
	// transaction; capture the prior value, disable it for the rebuild, and
	// restore it before the connection goes back to the pool.
	var fkPrior int
	_ = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fkPrior)
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("disable foreign keys: %w", err)
	}
	defer conn.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys=%d", fkPrior))

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rebuild tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Column adds run first, inside the same transaction, so the rebuilt
	// schema below includes them.
	for _, s := range preStmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("add column failed (%s): %w", s, err)
		}
	}

	// Original schema + the DDL of explicit indexes/triggers, captured before
	// the drop removes their sqlite_master rows.
	var createSQL string
	if err := tx.QueryRowContext(ctx,
		"SELECT sql FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&createSQL); err != nil {
		return fmt.Errorf("read schema for %q: %w", name, err)
	}
	rows, err := tx.QueryContext(ctx,
		"SELECT sql FROM sqlite_master WHERE tbl_name=? AND type IN ('index','trigger') AND sql IS NOT NULL", name)
	if err != nil {
		return fmt.Errorf("read indexes/triggers for %q: %w", name, err)
	}
	var auxDDL []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return fmt.Errorf("scan index/trigger ddl: %w", err)
		}
		auxDDL = append(auxDDL, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read indexes/triggers for %q: %w", name, err)
	}

	open := strings.Index(createSQL, "(")
	closeIdx := strings.LastIndex(createSQL, ")")
	if open < 0 || closeIdx <= open {
		return fmt.Errorf("cannot parse stored CREATE TABLE for %q", name)
	}
	body := createSQL[open+1 : closeIdx]
	suffix := strings.TrimSpace(createSQL[closeIdx+1:])

	var extra strings.Builder
	for _, ck := range checks {
		extra.WriteString(", CONSTRAINT ")
		extra.WriteString(quoteIdentifier(ck.name, m.driver))
		extra.WriteString(" CHECK (")
		extra.WriteString(ck.expr)
		extra.WriteString(")")
	}

	tmp := name + "_velocity_rebuild"
	newCreate := "CREATE TABLE " + quoteIdentifier(tmp, m.driver) + " (" + body + extra.String() + ")"
	if suffix != "" {
		newCreate += " " + suffix
	}

	steps := []string{
		newCreate,
		"INSERT INTO " + quoteIdentifier(tmp, m.driver) + " SELECT * FROM " + quoteIdentifier(name, m.driver),
		"DROP TABLE " + quoteIdentifier(name, m.driver),
		"ALTER TABLE " + quoteIdentifier(tmp, m.driver) + " RENAME TO " + quoteIdentifier(name, m.driver),
	}
	steps = append(steps, auxDDL...) // index/trigger DDL references name; valid post-rename
	for _, s := range steps {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("rebuild step failed (%s): %w", s, err)
		}
	}

	// PRAGMA foreign_key_check reports violations as result ROWS, not an
	// error, so it must be queried rather than Exec'd.
	fkRows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("foreign key check after rebuild: %w", err)
	}
	violated := fkRows.Next()
	fkErr := fkRows.Err()
	fkRows.Close()
	if fkErr != nil {
		return fmt.Errorf("foreign key check after rebuild: %w", fkErr)
	}
	if violated {
		return fmt.Errorf("rebuild of %q would violate foreign key constraints", name)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rebuild: %w", err)
	}
	committed = true
	return nil
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

	colSQL, err := builder.ToSQL()
	if err != nil {
		return err
	}
	sql := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s", quoteIdentifier(table, m.driver), colSQL)
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
	dims       int // For vector types
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

// Vector sets the column type to pgvector vector(dimensions). PostgreSQL only.
func (c *ColumnBuilder) Vector(dimensions int) *ColumnBuilder {
	c.colType = "vector"
	c.dims = dimensions
	return c
}

// BigInteger sets the column type to BIGINT
func (c *ColumnBuilder) BigInteger() *ColumnBuilder {
	c.colType = "biginteger"
	return c
}

// SmallInteger sets the column type to SMALLINT (INTEGER on SQLite)
func (c *ColumnBuilder) SmallInteger() *ColumnBuilder {
	c.colType = "smallinteger"
	return c
}

// Binary sets the column type to a binary blob (BYTEA/LONGBLOB/BLOB)
func (c *ColumnBuilder) Binary() *ColumnBuilder {
	c.colType = "binary"
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

// TimestampTz sets the column type to timestamp-with-time-zone
// (TIMESTAMPTZ/TIMESTAMP/DATETIME by driver)
func (c *ColumnBuilder) TimestampTz() *ColumnBuilder {
	c.colType = "timestamptz"
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

// DefaultRaw sets a raw SQL expression as the column default, emitted unquoted
// (e.g. "gen_random_uuid()", "now()").
//
// WARNING: the expression is inlined into DDL verbatim with no escaping or
// validation. Pass only trusted, developer-authored SQL, never user input.
func (c *ColumnBuilder) DefaultRaw(expr string) *ColumnBuilder {
	c.defValue = rawExpr(expr)
	c.hasDefault = true
	return c
}

// Unique marks the column as having a unique constraint
func (c *ColumnBuilder) Unique() *ColumnBuilder {
	c.unique = true
	return c
}

// ToSQL generates the column definition SQL fragment. It returns an error when
// the column cannot be expressed on the active driver (e.g. a vector column on
// a non-postgres driver, or an out-of-range dimension), so direct ColumnBuilder
// use fails loudly instead of degrading a vector column to TEXT.
func (c *ColumnBuilder) ToSQL() (string, error) {
	if err := validateVectorColumn(Column{Name: c.name, Type: c.colType, Dimensions: c.dims}, c.driver); err != nil {
		return "", err
	}

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

	return sql, nil
}

func (c *ColumnBuilder) toSQLiteType() string {
	switch c.colType {
	case "integer":
		return "INTEGER"
	case "biginteger":
		return "INTEGER"
	case "smallinteger":
		return "INTEGER"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "binary":
		return "BLOB"
	case "boolean":
		return "INTEGER"
	case "timestamp", "timestamptz":
		// No managed default here: SQLite's ALTER TABLE ADD COLUMN rejects a
		// non-constant default (CURRENT_TIMESTAMP) and also rejects NOT NULL
		// without a constant default, so a non-null timestamp simply cannot be
		// added to an existing SQLite table. Postgres/MySQL accept a volatile
		// default on ADD COLUMN, so they get the managed one (see those
		// type methods).
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
	case "smallinteger":
		return "SMALLINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "binary":
		return "BYTEA"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		if !c.nullable && !c.hasDefault {
			return "TIMESTAMP DEFAULT NOW()"
		}
		return "TIMESTAMP"
	case "timestamptz":
		if !c.nullable && !c.hasDefault {
			return "TIMESTAMPTZ DEFAULT NOW()"
		}
		return "TIMESTAMPTZ"
	case "date":
		return "DATE"
	case "uuid":
		return "UUID"
	case "vector":
		return fmt.Sprintf("vector(%d)", c.dims)
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
	case "smallinteger":
		return "SMALLINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", c.length)
	case "text":
		return "TEXT"
	case "binary":
		return "LONGBLOB"
	case "boolean":
		return "BOOLEAN"
	case "timestamp", "timestamptz":
		if !c.nullable && !c.hasDefault {
			return "TIMESTAMP DEFAULT CURRENT_TIMESTAMP"
		}
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
	lastColumn          *Column           // Track last column for chaining modifiers
	compositePrimaryKey []string          // For composite primary keys
	checks              []checkConstraint // Table-level CHECK constraints
}

// checkConstraint is a named table-level CHECK constraint. The expression is
// emitted verbatim (see TableBuilder.Check), so callers are responsible for its
// correctness; the name is validated as a DDL identifier by CreateTable.
type checkConstraint struct {
	name string
	expr string
}

// rawExpr marks a column default that must be emitted as unquoted SQL rather
// than a quoted literal (e.g. gen_random_uuid(), now(), '[]'::jsonb). It flows
// through Column.Default / ColumnBuilder default and is recognised by
// formatDefaultValue. Set it via DefaultRaw, never by quoting user input.
type rawExpr string

// Column represents a table column definition
type Column struct {
	Name          string
	Type          string
	Length        int
	Precision     int // For decimal types
	Scale         int // For decimal types
	Dimensions    int // For vector types
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

// BigID adds an auto-increment BIGINT primary key column named 'id'.
// Postgres emits BIGSERIAL, MySQL BIGINT AUTO_INCREMENT, SQLite INTEGER
// PRIMARY KEY AUTOINCREMENT (SQLite rowids are always 64-bit). Use this over
// ID() when the table is expected to exceed ~2.1 billion rows.
func (t *TableBuilder) BigID() *TableBuilder {
	col := Column{
		Name:          "id",
		Type:          "biginteger",
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

// TimestampTz adds a single timestamp-with-time-zone column. Postgres emits
// TIMESTAMPTZ; MySQL maps it to TIMESTAMP (which it stores in UTC and converts
// per session zone); SQLite has no zone-aware type so it falls back to
// DATETIME. Like Timestamp, a non-nullable column receives a DEFAULT of the
// current time; mark it Nullable() to omit that.
func (t *TableBuilder) TimestampTz(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "timestamptz",
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

// SmallInteger adds a SMALLINT column (16-bit). SQLite stores it as INTEGER
// (all integer widths share one storage class).
func (t *TableBuilder) SmallInteger(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "smallinteger",
		Nullable: false,
	}
	t.columns = append(t.columns, col)
	t.lastColumn = &t.columns[len(t.columns)-1]
	return t
}

// Binary adds a binary blob column. Postgres emits BYTEA, MySQL LONGBLOB,
// SQLite BLOB.
func (t *TableBuilder) Binary(name string) *TableBuilder {
	col := Column{
		Name:     name,
		Type:     "binary",
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

// Vector creates a pgvector column of the given fixed dimension,
// e.g. Vector("embedding", 1536) = vector(1536). PostgreSQL only (the column
// requires the pgvector extension; see Migrator.CreateVectorExtension). The
// dimension is validated when the table is created/altered (1..16000, the
// pgvector limit); creating a vector column on a non-postgres driver is a
// migration error. Chains the usual modifiers: Vector("e", 768).Nullable().
func (t *TableBuilder) Vector(name string, dimensions int) *TableBuilder {
	col := Column{
		Name:       name,
		Type:       "vector",
		Dimensions: dimensions,
		Nullable:   false,
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

// TimestampsTz adds created_at and updated_at columns typed as
// timestamp-with-time-zone (see TimestampTz for per-driver mapping).
func (t *TableBuilder) TimestampsTz() *TableBuilder {
	createdAt := Column{
		Name:     "created_at",
		Type:     "timestamptz",
		Nullable: false,
	}
	updatedAt := Column{
		Name:     "updated_at",
		Type:     "timestamptz",
		Nullable: false,
	}
	t.columns = append(t.columns, createdAt, updatedAt)
	// Don't set lastColumn since this adds multiple columns.
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

// DefaultRaw sets a raw SQL expression as the previous column's default,
// emitted unquoted (e.g. "gen_random_uuid()", "now()", "'[]'::jsonb").
//
// WARNING: the expression is inlined into DDL verbatim with no escaping or
// validation. Pass only trusted, developer-authored SQL, never user input.
// Use Default for ordinary literal values, which are quoted/escaped for you.
//
// On timestamp/timestamptz columns an explicit default set here overrides the
// builder's managed CURRENT_TIMESTAMP/NOW() default.
func (t *TableBuilder) DefaultRaw(expr string) *TableBuilder {
	if t.lastColumn != nil {
		t.lastColumn.Default = rawExpr(expr)
	}
	return t
}

// Check adds a named table-level CHECK constraint.
//
// WARNING: the expression is inlined into DDL verbatim with no escaping or
// validation. Pass only trusted, developer-authored SQL, never user input.
// The name is validated as a DDL identifier when the table is created.
func (t *TableBuilder) Check(name, expr string) *TableBuilder {
	t.checks = append(t.checks, checkConstraint{name: name, expr: expr})
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

// extraClauses returns the table-level clauses that render after the column
// list (composite PRIMARY KEY first, then CHECK constraints), already
// quoted/escaped for the active driver. Returned in deterministic order so DDL
// output is stable. CHECK expressions are emitted verbatim (see Check).
func (t *TableBuilder) extraClauses() []string {
	var clauses []string
	if len(t.compositePrimaryKey) > 0 {
		parts := make([]string, len(t.compositePrimaryKey))
		for i, col := range t.compositePrimaryKey {
			parts[i] = quoteIdentifier(col, t.driver)
		}
		clauses = append(clauses, "PRIMARY KEY ("+strings.Join(parts, ", ")+")")
	}
	for _, ck := range t.checks {
		clauses = append(clauses, "CONSTRAINT "+quoteIdentifier(ck.name, t.driver)+" CHECK ("+ck.expr+")")
	}
	return clauses
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

	extras := t.extraClauses()

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
			// SQLite uses INTEGER for all int sizes; its rowid PK is 64-bit.
			if col.PrimaryKey && col.AutoIncrement {
				sql += "INTEGER PRIMARY KEY AUTOINCREMENT"
			} else {
				sql += "INTEGER"
			}
		case "smallinteger":
			sql += "INTEGER" // SQLite has one integer storage class
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "binary":
			sql += "BLOB"
		case "boolean":
			sql += "INTEGER" // SQLite uses 0/1 for boolean
		case "timestamp", "timestamptz":
			sql += "DATETIME" // SQLite has no zone-aware type
			// Managed default only when the caller did not set one; an
			// explicit Default/DefaultRaw is emitted by the generic block.
			if !col.Nullable && col.Default == nil {
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

		if col.Default != nil {
			sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "sqlite")
		}

		if i < len(t.columns)-1 || len(extras) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Table-level clauses (composite PRIMARY KEY, CHECK constraints).
	for j, clause := range extras {
		sql += "  " + clause
		if j < len(extras)-1 {
			sql += ","
		}
		sql += "\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toPostgresSyntax() string {
	sql := "CREATE TABLE " + quoteIdentifier(t.tableName, t.driver) + " (\n"

	extras := t.extraClauses()

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
			if col.PrimaryKey && col.AutoIncrement {
				sql += "BIGSERIAL PRIMARY KEY"
			} else {
				sql += "BIGINT"
			}
		case "smallinteger":
			sql += "SMALLINT"
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "binary":
			sql += "BYTEA"
		case "boolean":
			sql += "BOOLEAN"
		case "timestamp":
			sql += "TIMESTAMP"
			if !col.Nullable && col.Default == nil {
				sql += " DEFAULT NOW()"
			}
		case "timestamptz":
			sql += "TIMESTAMPTZ"
			if !col.Nullable && col.Default == nil {
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
		case "vector":
			sql += fmt.Sprintf("vector(%d)", col.Dimensions)
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

			if col.Default != nil {
				sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "postgres")
			}
		}

		if i < len(t.columns)-1 || len(extras) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Table-level clauses (composite PRIMARY KEY, CHECK constraints).
	for j, clause := range extras {
		sql += "  " + clause
		if j < len(extras)-1 {
			sql += ","
		}
		sql += "\n"
	}

	sql += ")"
	return sql
}

func (t *TableBuilder) toMySQLSyntax() string {
	sql := "CREATE TABLE " + quoteIdentifier(t.tableName, t.driver) + " (\n"

	extras := t.extraClauses()

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
			if col.PrimaryKey && col.AutoIncrement {
				sql += "BIGINT AUTO_INCREMENT PRIMARY KEY"
			} else {
				sql += "BIGINT"
			}
		case "smallinteger":
			sql += "SMALLINT"
		case "string":
			sql += "VARCHAR(" + fmt.Sprintf("%d", col.Length) + ")"
		case "text":
			sql += "TEXT"
		case "binary":
			sql += "LONGBLOB"
		case "boolean":
			sql += "BOOLEAN"
		case "timestamp", "timestamptz":
			// MySQL has no separate zone-aware type; TIMESTAMP is stored in
			// UTC and converted per session time zone.
			sql += "TIMESTAMP"
			if !col.Nullable && col.Default == nil {
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

			if col.Default != nil {
				sql += " DEFAULT " + formatDefaultValue(col.Default, col.Type, "mysql")
			}
		}

		if i < len(t.columns)-1 || len(extras) > 0 {
			sql += ","
		}
		sql += "\n"
	}

	// Table-level clauses (composite PRIMARY KEY, CHECK constraints).
	for j, clause := range extras {
		sql += "  " + clause
		if j < len(extras)-1 {
			sql += ","
		}
		sql += "\n"
	}

	sql += ")"
	return sql
}

// pgvectorMaxDimensions is the maximum dimension count pgvector permits for the
// vector type.
const pgvectorMaxDimensions = 16000

// validateVectorColumn enforces pgvector's constraints for a vector column:
// postgres only, with a dimension in [1, pgvectorMaxDimensions]. It returns nil
// for non-vector columns. The dimension is rendered into DDL via fmt %d, so
// this is the guard that prevents emitting vector(0)/vector(-1) or a vector type
// on a dialect that cannot parse it.
func validateVectorColumn(col Column, driver string) error {
	if col.Type != "vector" {
		return nil
	}
	if driver != "postgres" {
		return fmt.Errorf("vector column %q is only supported on postgres (driver %q)", col.Name, driver)
	}
	if col.Dimensions < 1 || col.Dimensions > pgvectorMaxDimensions {
		return fmt.Errorf("vector column %q: dimensions must be between 1 and %d, got %d", col.Name, pgvectorMaxDimensions, col.Dimensions)
	}
	return nil
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
	if col.Default != nil {
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
	case "smallinteger":
		return "INTEGER"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "binary":
		return "BLOB"
	case "boolean":
		return "INTEGER"
	case "timestamp", "timestamptz":
		s := "DATETIME"
		if !col.Nullable && col.Default == nil {
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
	case "smallinteger":
		return "SMALLINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "binary":
		return "BYTEA"
	case "boolean":
		return "BOOLEAN"
	case "timestamp":
		s := "TIMESTAMP"
		if !col.Nullable && col.Default == nil {
			s += " DEFAULT NOW()"
		}
		return s
	case "timestamptz":
		s := "TIMESTAMPTZ"
		if !col.Nullable && col.Default == nil {
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
	case "vector":
		return fmt.Sprintf("vector(%d)", col.Dimensions)
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
	case "smallinteger":
		return "SMALLINT"
	case "string":
		return fmt.Sprintf("VARCHAR(%d)", col.Length)
	case "text":
		return "TEXT"
	case "binary":
		return "LONGBLOB"
	case "boolean":
		return "BOOLEAN"
	case "timestamp", "timestamptz":
		s := "TIMESTAMP"
		if !col.Nullable && col.Default == nil {
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
	case rawExpr:
		// Raw SQL expression, emitted unquoted (see DefaultRaw). Caller owns
		// its correctness and safety.
		return string(v)
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
