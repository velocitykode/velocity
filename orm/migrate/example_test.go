package migrate_test

import (
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

func init() {
	// Register example migration
	migrate.Register(&migrate.Migration{
		Version: "20251009120000",
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable("users", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("email").Unique()
				t.String("name")
				t.Timestamps()
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("users")
		},
	})
}

func newTestORM(t *testing.T) *orm.Manager {
	t.Helper()
	m, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	return m
}

func TestMigrationBasic(t *testing.T) {
	m := newTestORM(t)
	defer m.Close()

	// Create migrator
	migrator := migrate.NewMigrator(m.DB(), m.DriverName())

	// Run migrations
	err := migrator.Up()
	if err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Verify table exists
	db := m.DB()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query table: %v", err)
	}

	if count != 1 {
		t.Errorf("expected users table to exist, got count=%d", count)
	}

	// Test migration status
	statuses, err := migrator.Status()
	if err != nil {
		t.Fatalf("failed to get status: %v", err)
	}

	if len(statuses) == 0 {
		t.Error("expected at least one migration status")
	}

	t.Logf("Migration system working! Found %d migrations", len(statuses))
}
