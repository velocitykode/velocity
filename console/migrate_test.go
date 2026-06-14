package console

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// migrateTestVersion is unique to the console package so it never collides with
// another Register call. The registry is process-global and Register panics on
// duplicate versions, so this migration is registered exactly once (init) and
// makes migrate.All() non-empty for the whole package.
const (
	migrateTestVersion     = "20991231235959"
	migrateTestDescription = "console_migrate_test"
	migrateTestTable       = "console_migrate_test"
)

func init() {
	migrate.Register(&migrate.Migration{
		Version:     migrateTestVersion,
		Description: migrateTestDescription,
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable(migrateTestTable, func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable(migrateTestTable)
		},
	})
}

func newMigrateTestManager(t *testing.T) *orm.Manager {
	t.Helper()
	mgr, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("orm.NewManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Shutdown(context.Background()) })
	return mgr
}

// migrateTestState returns the State the migrator reports for our test version.
func migrateTestState(t *testing.T, db orm.Database) string {
	t.Helper()
	statuses, err := migrate.NewMigrator(db.DB(), db.DriverName()).Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, s := range statuses {
		if s.Version == migrateTestVersion {
			return s.State
		}
	}
	t.Fatalf("test migration %s not found in status", migrateTestVersion)
	return ""
}

func TestMigrate_HappyPath(t *testing.T) {
	db := newMigrateTestManager(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if state := migrateTestState(t, db); state != "Applied" {
		t.Fatalf("expected migration Applied after Migrate, got %q", state)
	}
}

func TestMigrateRollback_HappyPath(t *testing.T) {
	db := newMigrateTestManager(t)

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := MigrateRollback(db, 1); err != nil {
		t.Fatalf("MigrateRollback: %v", err)
	}

	if state := migrateTestState(t, db); state != "Pending" {
		t.Fatalf("expected migration Pending after rollback, got %q", state)
	}
}

func TestMigrateFresh_PrintsOnlyApplied(t *testing.T) {
	db := newMigrateTestManager(t)

	var buf bytes.Buffer
	prism.SetWriter(&buf)
	defer prism.SetWriter(nil)

	if err := MigrateFresh(db); err != nil {
		t.Fatalf("MigrateFresh: %v", err)
	}

	want := migrateTestVersion + "_" + migrateTestDescription
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("expected output to list applied migration %q, got:\n%s", want, buf.String())
	}
	if state := migrateTestState(t, db); state != "Applied" {
		t.Fatalf("expected migration Applied after Fresh, got %q", state)
	}
}

// TestMigrate_DBClosedReturnsError is the regression guard for B34: the old
// hand-rolled getPendingMigrations swallowed the query failure and reported
// everything as pending. The migrate.Pending path must surface the error.
func TestMigrate_DBClosedReturnsError(t *testing.T) {
	db := newMigrateTestManager(t)
	if err := db.DB().Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := Migrate(db); err == nil {
		t.Fatal("expected Migrate to return an error on a closed database, got nil")
	}
}

// TestMigrateRollback_DBClosedReturnsError is the regression guard for the old
// getRollbackMigrations, which swallowed the query failure and printed
// "Nothing to rollback" while returning nil. Status must surface the error.
func TestMigrateRollback_DBClosedReturnsError(t *testing.T) {
	db := newMigrateTestManager(t)
	if err := db.DB().Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := MigrateRollback(db, 1); err == nil {
		t.Fatal("expected MigrateRollback to return an error on a closed database, got nil")
	}
}
