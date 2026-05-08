package testing_test

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
	ormtesting "github.com/velocitykode/velocity/orm/testing"
)

func init() {
	// Register migration for testing
	migrate.Register(&migrate.Migration{
		Version: "20251009120000",
		Up: func(m *migrate.Migrator) error {
			return m.CreateTable("users", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
				t.String("email")
				t.Timestamps()
			})
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("users")
		},
	})
}

func newExampleTestManager(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to create ORM manager: %v", err)
	}
	return manager
}

func UserFactory(manager *orm.Manager) *ormtesting.Factory {
	faker := ormtesting.Faker()
	return ormtesting.NewFactory(manager, "users", func() map[string]interface{} {
		return map[string]interface{}{
			"name":  faker.Name(),
			"email": faker.Email(),
		}
	})
}

func TestFactoryMake(t *testing.T) {
	user := UserFactory(nil).Make()

	userMap, ok := user.(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}

	if userMap["name"] == nil {
		t.Error("expected name to be set")
	}

	if userMap["email"] == nil {
		t.Error("expected email to be set")
	}

	t.Logf("Factory Make() working! Generated: %+v", userMap)
}

func TestFactoryCreate(t *testing.T) {
	manager := newExampleTestManager(t)
	defer manager.Shutdown(context.Background())

	// Run migrations
	migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
	err := migrator.Up()
	if err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	// Create user via factory
	user := UserFactory(manager).Create(context.Background())

	userMap, ok := user.(map[string]interface{})
	if !ok {
		t.Fatal("expected map[string]interface{}")
	}

	if userMap["id"] == nil {
		t.Error("expected id to be set after Create()")
	}

	// Verify in database
	db := manager.DB()
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query users: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 user in database, got %d", count)
	}

	t.Logf("Factory Create() working! User ID: %v", userMap["id"])
}

func TestRefreshDatabase(t *testing.T) {
	manager := newExampleTestManager(t)
	defer manager.Shutdown(context.Background())

	// Use RefreshDatabase
	db := ormtesting.RefreshDatabase(t, manager)
	if db == nil {
		t.Fatal("expected database connection")
	}

	// Create a user
	UserFactory(manager).Create(context.Background())

	// Verify table exists and has data
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query users: %v", err)
	}

	if count != 1 {
		t.Errorf("expected 1 user, got %d", count)
	}

	t.Log("RefreshDatabase() working! Migrations ran, factory created data")
}
