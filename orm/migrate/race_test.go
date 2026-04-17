package migrate

import (
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// Test-only migration counter. Each Up() call increments this so a race
// test can assert that concurrent migrators still execute the migration
// exactly once.
var raceMigrationRuns atomic.Int32

func init() {
	Register(&Migration{
		Version:     "20260101000001",
		Description: "race-test migration",
		Up: func(m *Migrator) error {
			raceMigrationRuns.Add(1)
			return m.CreateTable("race_test_users", func(t *TableBuilder) {
				t.ID()
				t.String("email")
			})
		},
		Down: func(m *Migrator) error {
			return m.DropTable("race_test_users")
		},
	})
}

// TestMigrator_Up_RaceSafe runs Up() concurrently from two goroutines
// sharing the same *sql.DB. The advisory-lock strategy (pg_advisory_lock
// on Postgres, row-level lock on MySQL/SQLite) must guarantee exactly-
// once execution of the pending migration.
//
// SQLite is used here because it is the only driver the test harness can
// bring up without external infrastructure, and its serialized write
// model is the hardest case for our row-lock strategy. Success on
// SQLite is a strong proxy for MySQL behaviour; Postgres uses a distinct
// mechanism (pg_advisory_lock) exercised by integration tests.
func TestMigrator_Up_RaceSafe(t *testing.T) {
	// Shared DB handle so both goroutines contend on the same SQLite
	// file. A disk-backed file (vs. :memory:) ensures all connections
	// in the pool see the same schema, which is required for the
	// compare-and-set lock strategy to block the second goroutine.
	dbPath := filepath.Join(t.TempDir(), "race.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(4)

	// Drop any residue from prior test runs sharing this process.
	_, _ = db.Exec("DROP TABLE IF EXISTS race_test_users")
	_, _ = db.Exec("DROP TABLE IF EXISTS migrations")
	_, _ = db.Exec("DROP TABLE IF EXISTS migrations_lock")

	// Reset the counter so prior tests do not skew this assertion.
	raceMigrationRuns.Store(0)

	var (
		wg     sync.WaitGroup
		errs   [2]error
		start  = make(chan struct{})
		didRun [2]bool
	)

	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			migrator := NewMigrator(db, "sqlite")
			<-start
			errs[i] = migrator.Up()
			didRun[i] = true
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d Up() returned error: %v", i, err)
		}
		if !didRun[i] {
			t.Errorf("goroutine %d did not run", i)
		}
	}

	// The critical assertion: the migration body ran exactly once, even
	// though both goroutines called Up(). Everything else — migration
	// row count, table existence — is a downstream consequence of this.
	if got := raceMigrationRuns.Load(); got != 1 {
		t.Fatalf("migration Up() body ran %d times; want 1 (race condition!)", got)
	}

	// Double-check the migrations table records exactly one applied row.
	var applied int
	if err := db.QueryRow("SELECT COUNT(*) FROM migrations WHERE version = ?", "20260101000001").Scan(&applied); err != nil {
		t.Fatalf("query migrations: %v", err)
	}
	if applied != 1 {
		t.Errorf("migrations table contains %d rows for version; want 1", applied)
	}
}
