package migrate

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/lib/pq"
)

// pgRaceMigrationRuns counts how many times the Postgres race-test
// migration body has executed across all migrator goroutines. The
// advisory-lock contract requires this to be exactly 1 when multiple
// migrators run concurrently against the same database.
var pgRaceMigrationRuns atomic.Int32

func init() {
	// Use a distinct version so this migration does not collide with the
	// SQLite race test that registers "20260101000001".
	Register(&Migration{
		Version:     "20260101000002",
		Description: "postgres advisory-lock race-test migration",
		Up: func(m *Migrator) error {
			pgRaceMigrationRuns.Add(1)
			return m.CreateTable("pg_lock_test_users", func(t *TableBuilder) {
				t.ID()
				t.String("email")
			})
		},
		Down: func(m *Migrator) error {
			return m.DropTable("pg_lock_test_users")
		},
	})
}

// openPostgresForTest opens a *sql.DB against the test Postgres instance,
// or skips the calling test if no Postgres connection is configured. The
// env var convention mirrors orm/raw_query_test.go.
func openPostgresForTest(t *testing.T) *sql.DB {
	t.Helper()

	if os.Getenv("TEST_POSTGRES") == "" {
		t.Skip("Skipping Postgres advisory-lock test (set TEST_POSTGRES=1 to run)")
	}

	host := os.Getenv("POSTGRES_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("POSTGRES_PORT")
	if port == "" {
		port = "5432"
	}
	database := os.Getenv("POSTGRES_DB")
	if database == "" {
		database = "velocity_test"
	}
	user := os.Getenv("POSTGRES_USER")
	if user == "" {
		user = "postgres"
	}
	pass := os.Getenv("POSTGRES_PASSWORD")

	dsn := fmt.Sprintf(
		"host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		host, port, database, user, pass,
	)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("Skipping Postgres test, sql.Open failed: %v", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Skipf("Skipping Postgres test, ping failed: %v", err)
	}
	return db
}

// TestMigrator_Postgres_AdvisoryLockReleased verifies the fix for the
// session-scoped advisory-lock bug. Before the fix, lock and unlock were
// issued via *sql.DB on potentially different pooled connections; the
// unlock would no-op and pg_advisory_lock would leak the lock until the
// holding session terminated. After the fix, lock+unlock run on a
// pinned *sql.Conn and the lock is reliably released when Up() returns.
//
// The assertion queries pg_locks for any advisory lock matching the
// migrator's key. A clean state after Up() proves the unlock landed on
// the same backend that took the lock.
func TestMigrator_Postgres_AdvisoryLockReleased(t *testing.T) {
	db := openPostgresForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	// Drop any residue so the migration body actually has to run.
	_, _ = db.Exec(`DROP TABLE IF EXISTS pg_lock_test_users CASCADE`)
	_, _ = db.Exec(`DROP TABLE IF EXISTS migrations CASCADE`)

	pgRaceMigrationRuns.Store(0)

	migrator := NewMigrator(db, "postgres")
	if err := migrator.Up(); err != nil {
		t.Fatalf("Up() returned error: %v", err)
	}

	// pg_locks exposes one row per held advisory lock. The bug would
	// leave a row here keyed on migrationLockKey because the unlock
	// landed on a different backend than the lock.
	var held int
	row := db.QueryRow(
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1`,
		// Postgres advisory locks split int64 into two int32 fields
		// (classid, objid); for a single-int key the objid stores the
		// low 32 bits. We instead match on the full bigint via
		// classid<<32 | objid using the encoding pg_advisory_lock(bigint)
		// produces: classid = high 32 bits, objid = low 32 bits.
		int32(migrationLockKey&0xFFFFFFFF),
	)
	if err := row.Scan(&held); err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	if held != 0 {
		t.Fatalf("advisory lock leaked: %d row(s) in pg_locks after Up() returned; want 0", held)
	}

	// Sanity check: the migration body should have run exactly once.
	if got := pgRaceMigrationRuns.Load(); got != 1 {
		t.Fatalf("migration body ran %d times; want 1", got)
	}
}

// TestMigrator_Postgres_ConcurrentUpRaceSafe is the multi-process variant:
// two migrators sharing the same *sql.DB call Up() concurrently. The
// pinned-conn fix must serialize them through pg_advisory_lock so the
// migration body still runs exactly once.
func TestMigrator_Postgres_ConcurrentUpRaceSafe(t *testing.T) {
	db := openPostgresForTest(t)
	t.Cleanup(func() { _ = db.Close() })

	_, _ = db.Exec(`DROP TABLE IF EXISTS pg_lock_test_users CASCADE`)
	_, _ = db.Exec(`DROP TABLE IF EXISTS migrations CASCADE`)

	pgRaceMigrationRuns.Store(0)

	// Generous pool so both goroutines can hold their own conn while
	// the second one blocks on pg_advisory_lock.
	db.SetMaxOpenConns(8)

	var (
		wg    sync.WaitGroup
		errs  [2]error
		start = make(chan struct{})
	)
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			migrator := NewMigrator(db, "postgres")
			<-start
			errs[i] = migrator.Up()
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d Up() returned error: %v", i, err)
		}
	}
	if got := pgRaceMigrationRuns.Load(); got != 1 {
		t.Fatalf("migration body ran %d times; want 1 (lock did not serialize callers)", got)
	}

	// Final invariant: no advisory lock leaked.
	var held int
	if err := db.QueryRow(
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = $1`,
		int32(migrationLockKey&0xFFFFFFFF),
	).Scan(&held); err != nil {
		t.Fatalf("query pg_locks: %v", err)
	}
	if held != 0 {
		t.Fatalf("advisory lock leaked: %d row(s) in pg_locks after concurrent Up()", held)
	}
}
