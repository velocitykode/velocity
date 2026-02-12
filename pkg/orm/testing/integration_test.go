package testing_test

import (
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
	ormtesting "github.com/velocitykode/velocity/pkg/orm/testing"
)

// Test across all database drivers
func TestCrossDatabaseMigrations(t *testing.T) {
	drivers := []struct {
		name   string
		config orm.ManagerConfig
		skip   bool
	}{
		{
			name:   "sqlite",
			config: orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"},
			skip:   false,
		},
		{
			name:   "mysql",
			config: orm.ManagerConfig{Driver: "mysql", Database: "velocity_test"},
			skip:   true, // Skip if MySQL not available
		},
		{
			name:   "postgres",
			config: orm.ManagerConfig{Driver: "postgres", Database: "velocity_test"},
			skip:   true, // Skip if Postgres not available
		},
	}

	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			if d.skip {
				t.Skip("Database server not available")
			}

			// Initialize ORM with this driver
			manager, err := orm.NewManager(d.config)
			if err != nil {
				t.Skipf("Failed to init %s (server may not be running): %v", d.name, err)
			}
			defer manager.Close()

			// Create migrator
			migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())

			// Run migrations
			err = migrator.Up()
			if err != nil {
				t.Fatalf("%s: failed to run migrations: %v", d.name, err)
			}

			// Verify table exists
			db := manager.DB()
			tables, err := ormtesting.GetAllTables(db, d.name)
			if err != nil {
				t.Fatalf("%s: failed to get tables: %v", d.name, err)
			}

			// Should have migrations table + users table
			if len(tables) < 2 {
				t.Errorf("%s: expected at least 2 tables (migrations + users), got %d", d.name, len(tables))
			}

			t.Logf("%s: Migrations working - created %d tables", d.name, len(tables))
		})
	}
}

func TestCrossDatabaseFactories(t *testing.T) {
	drivers := []struct {
		name   string
		config orm.ManagerConfig
		skip   bool
	}{
		{
			name:   "sqlite",
			config: orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"},
			skip:   false,
		},
		{
			name:   "mysql",
			config: orm.ManagerConfig{Driver: "mysql", Database: "velocity_test"},
			skip:   true,
		},
		{
			name:   "postgres",
			config: orm.ManagerConfig{Driver: "postgres", Database: "velocity_test"},
			skip:   true,
		},
	}

	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			if d.skip {
				t.Skip("Database server not available")
			}

			// Initialize
			manager, err := orm.NewManager(d.config)
			if err != nil {
				t.Skipf("Failed to init %s: %v", d.name, err)
			}
			defer manager.Close()

			// Run migrations
			migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
			if err := migrator.Up(); err != nil {
				t.Fatalf("%s: failed to run migrations: %v", d.name, err)
			}

			// Create user via factory
			user := UserFactory(manager).Create()
			userMap := user.(map[string]interface{})

			if userMap["id"] == nil {
				t.Errorf("%s: expected id to be set", d.name)
			}

			// Verify in database
			db := manager.DB()
			var count int
			err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			if err != nil {
				t.Fatalf("%s: failed to query users: %v", d.name, err)
			}

			if count != 1 {
				t.Errorf("%s: expected 1 user, got %d", d.name, count)
			}

			t.Logf("%s: Factory Create() working - inserted user ID %v", d.name, userMap["id"])
		})
	}
}

func TestCrossDatabaseRefresh(t *testing.T) {
	drivers := []struct {
		name   string
		config orm.ManagerConfig
		skip   bool
	}{
		{
			name:   "sqlite",
			config: orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"},
			skip:   false,
		},
		{
			name:   "mysql",
			config: orm.ManagerConfig{Driver: "mysql", Database: "velocity_test"},
			skip:   true,
		},
		{
			name:   "postgres",
			config: orm.ManagerConfig{Driver: "postgres", Database: "velocity_test"},
			skip:   true,
		},
	}

	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			if d.skip {
				t.Skip("Database server not available")
			}

			// Initialize
			manager, err := orm.NewManager(d.config)
			if err != nil {
				t.Skipf("Failed to init %s: %v", d.name, err)
			}
			defer manager.Close()

			// Use RefreshDatabase
			db := ormtesting.RefreshDatabase(t, manager)

			// Create some data
			UserFactory(manager).Count(5).Create()

			// Verify data exists
			var count int
			err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			if err != nil {
				t.Fatalf("%s: failed to query users: %v", d.name, err)
			}

			if count != 5 {
				t.Errorf("%s: expected 5 users, got %d", d.name, count)
			}

			// Verify migrations table exists
			err = db.QueryRow("SELECT COUNT(*) FROM migrations").Scan(&count)
			if err != nil {
				t.Fatalf("%s: failed to query migrations: %v", d.name, err)
			}

			if count == 0 {
				t.Errorf("%s: expected migrations to be recorded", d.name)
			}

			t.Logf("%s: RefreshDatabase working - tables dropped, migrations ran, factory created data", d.name)
		})
	}
}

// Test that placeholder syntax is correct for each driver
func TestCrossDatabasePlaceholders(t *testing.T) {
	drivers := []struct {
		name   string
		config orm.ManagerConfig
		skip   bool
	}{
		{
			name:   "sqlite",
			config: orm.ManagerConfig{Driver: "sqlite", Database: ":memory:"},
			skip:   false,
		},
		{
			name:   "mysql",
			config: orm.ManagerConfig{Driver: "mysql", Database: "velocity_test"},
			skip:   true,
		},
		{
			name:   "postgres",
			config: orm.ManagerConfig{Driver: "postgres", Database: "velocity_test"},
			skip:   true,
		},
	}

	for _, d := range drivers {
		t.Run(d.name, func(t *testing.T) {
			if d.skip {
				t.Skip("Database server not available")
			}

			// Initialize
			manager, err := orm.NewManager(d.config)
			if err != nil {
				t.Skipf("Failed to init %s: %v", d.name, err)
			}
			defer manager.Close()

			// Run migrations
			migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())
			if err := migrator.Up(); err != nil {
				t.Fatalf("%s: failed to run migrations: %v", d.name, err)
			}

			// Create 10 users with sequences (tests placeholders)
			users := UserFactory(manager).
				Count(10).
				Sequence("email", func(i int) interface{} {
					return "user" + string(rune(i)) + "@test.com"
				}).
				Create()

			userList := users.([]map[string]interface{})
			if len(userList) != 10 {
				t.Errorf("%s: expected 10 users, got %d", d.name, len(userList))
			}

			// Verify all inserted
			db := manager.DB()
			var count int
			err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
			if err != nil {
				t.Fatalf("%s: failed to count users: %v", d.name, err)
			}

			if count != 10 {
				t.Errorf("%s: expected 10 users in database, got %d", d.name, count)
			}

			t.Logf("%s: Placeholders working correctly - inserted 10 users", d.name)
		})
	}
}
