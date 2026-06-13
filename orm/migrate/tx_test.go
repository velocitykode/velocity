package migrate

import (
	"context"
	"fmt"
	"testing"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

// TestMigrator_execContext_RoutesThroughTxFirst proves the per-migration
// transaction plumbing: when m.tx is set, execContext/queryContext/
// queryRowContext must route through it ahead of both the pinned conn and
// the pooled db. The proof is rollback visibility: a write issued through
// execContext while m.tx is set, then rolled back, must NOT persist. If the
// helper had wrongly gone to m.conn or m.db (autocommit), the row would
// survive the rollback.
func TestMigrator_execContext_RoutesThroughTxFirst(t *testing.T) {
	db := openSQLiteForLockTest(t)
	if _, err := db.Exec("CREATE TABLE tx_route (id INTEGER PRIMARY KEY, v INTEGER)"); err != nil {
		t.Fatalf("create probe table: %v", err)
	}

	m := NewMigrator(db, "sqlite")
	ctx := context.Background()

	// Pin a conn too, so this also proves tx takes precedence over conn
	// (not just over db).
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("pin conn: %v", err)
	}
	defer conn.Close()
	m.conn = conn

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	m.tx = tx

	if _, err := m.execContext(ctx, "INSERT INTO tx_route (id, v) VALUES (1, 42)"); err != nil {
		t.Fatalf("execContext insert: %v", err)
	}

	// queryRowContext must observe the row inside the same tx.
	var v int
	if err := m.queryRowContext(ctx, "SELECT v FROM tx_route WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("queryRowContext inside tx: %v", err)
	}
	if v != 42 {
		t.Fatalf("queryRowContext read %d inside tx; want 42", v)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	m.tx = nil

	// The write went through the rolled-back tx, so it must be gone. A row
	// here means execContext bypassed the tx and hit conn/db directly.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM tx_route").Scan(&count); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("row survived rollback (count=%d); execContext did not route through m.tx", count)
	}
}

// TestMigrator_useTx_OnlyPostgres pins the policy that per-migration
// transactions wrap only Postgres, and never in pretend mode. MySQL DDL
// auto-commits (a wrapping tx is misleading) and SQLite is left unwrapped to
// avoid nesting against sqliteRebuildWithChecks.
func TestMigrator_useTx_OnlyPostgres(t *testing.T) {
	db := openSQLiteForLockTest(t)
	// A real, non-nil *sql.Conn used purely as useTx's nil-check sentinel;
	// its own driver is irrelevant since useTx only inspects m.driver.
	sentinel, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin sentinel conn: %v", err)
	}
	defer sentinel.Close()

	cases := []struct {
		driver  string
		pretend bool
		hasConn bool
		want    bool
	}{
		{"postgres", false, true, true},
		{"postgres", true, true, false},   // pretend never wraps
		{"postgres", false, false, false}, // no pinned conn, nothing to wrap on
		{"mysql", false, true, false},
		{"sqlite", false, true, false},
		{"unknown", false, true, false},
	}
	for _, c := range cases {
		m := &Migrator{driver: c.driver, pretend: c.pretend}
		if c.hasConn {
			m.conn = sentinel
		}
		if got := m.useTx(); got != c.want {
			t.Errorf("useTx(driver=%s pretend=%v hasConn=%v) = %v; want %v",
				c.driver, c.pretend, c.hasConn, got, c.want)
		}
	}
}

// TestMigrator_Postgres_MigrationRollsBackOnFailure exercises the real
// transactional-DDL path: a migration that creates a table and then fails
// must leave neither the table nor a migrations row. Gated behind
// TEST_POSTGRES exactly like postgres_lock_test.go.
func TestMigrator_Postgres_MigrationRollsBackOnFailure(t *testing.T) {
	db := openPostgresForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec("DROP TABLE IF EXISTS tx_rollback_probe CASCADE")
	_, _ = db.Exec("DROP TABLE IF EXISTS migrations CASCADE")

	m := NewMigrator(db, "postgres")
	if err := m.createMigrationsTable(); err != nil {
		t.Fatalf("create migrations table: %v", err)
	}

	// Pin the advisory-lock conn so useTx() is satisfied and the tx lives
	// on the same backend.
	release, err := m.acquireMigrationLock()
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer release()

	failing := Migration{
		Version: "29990101000000",
		Up: func(mi *Migrator) error {
			if err := mi.CreateTable("tx_rollback_probe", func(b *TableBuilder) {
				b.ID()
			}); err != nil {
				return err
			}
			return fmt.Errorf("boom: migration fails after creating table")
		},
	}

	if err := m.runMigrationUp(failing, 1); err == nil {
		t.Fatal("runMigrationUp returned nil; expected the migration to fail")
	}

	// Table create must have rolled back with the failure.
	var tables int
	if err := db.QueryRow(
		"SELECT count(*) FROM information_schema.tables WHERE table_name = 'tx_rollback_probe'",
	).Scan(&tables); err != nil {
		t.Fatalf("query tables: %v", err)
	}
	if tables != 0 {
		t.Fatalf("tx_rollback_probe survived a failed migration; transaction did not roll back DDL")
	}

	// And the migration must NOT be recorded as applied.
	var recorded int
	if err := db.QueryRow(
		"SELECT count(*) FROM migrations WHERE version = '29990101000000'",
	).Scan(&recorded); err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if recorded != 0 {
		t.Fatalf("failed migration was recorded as applied (%d rows); want 0", recorded)
	}
}

// TestMigrator_Postgres_MigrationCommitsOnSuccess is the positive companion:
// a successful migration leaves both the table and its migrations row.
func TestMigrator_Postgres_MigrationCommitsOnSuccess(t *testing.T) {
	db := openPostgresForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec("DROP TABLE IF EXISTS tx_commit_probe CASCADE")
	_, _ = db.Exec("DROP TABLE IF EXISTS migrations CASCADE")

	m := NewMigrator(db, "postgres")
	if err := m.createMigrationsTable(); err != nil {
		t.Fatalf("create migrations table: %v", err)
	}
	release, err := m.acquireMigrationLock()
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer release()

	ok := Migration{
		Version: "29990101000001",
		Up: func(mi *Migrator) error {
			return mi.CreateTable("tx_commit_probe", func(b *TableBuilder) {
				b.ID()
				b.String("name")
			})
		},
	}
	if err := m.runMigrationUp(ok, 1); err != nil {
		t.Fatalf("runMigrationUp: %v", err)
	}

	var tables int
	if err := db.QueryRow(
		"SELECT count(*) FROM information_schema.tables WHERE table_name = 'tx_commit_probe'",
	).Scan(&tables); err != nil {
		t.Fatalf("query tables: %v", err)
	}
	if tables != 1 {
		t.Fatalf("tx_commit_probe missing after a successful migration (count=%d)", tables)
	}

	var recorded int
	if err := db.QueryRow(
		"SELECT count(*) FROM migrations WHERE version = '29990101000001'",
	).Scan(&recorded); err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if recorded != 1 {
		t.Fatalf("successful migration not recorded (count=%d); want 1", recorded)
	}
}
