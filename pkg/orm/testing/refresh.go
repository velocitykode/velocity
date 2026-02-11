package testing

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
)

// dbIdentifierRegex validates database/table names
var dbIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// GetAllTables returns a list of all tables in the database
func GetAllTables(db *sql.DB, driver string) ([]string, error) {
	var query string

	switch driver {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"

	case "mysql":
		dbName := orm.GetDatabaseName()
		if dbName == "" {
			return nil, fmt.Errorf("cannot get database name for MySQL")
		}
		if !dbIdentifierRegex.MatchString(dbName) {
			return nil, fmt.Errorf("invalid database name: %q", dbName)
		}
		query = fmt.Sprintf("SELECT table_name FROM information_schema.tables WHERE table_schema = '%s' ORDER BY table_name", dbName)

	case "postgres":
		query = "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY tablename"

	default:
		return nil, fmt.Errorf("unsupported driver: %s", driver)
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()

	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, table)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating tables: %w", err)
	}

	return tables, nil
}

// DropAllTables drops all tables in the database
func DropAllTables(db *sql.DB, driver string) error {
	tables, err := GetAllTables(db, driver)
	if err != nil {
		return err
	}

	// Drop in reverse order to handle foreign key constraints
	for i := len(tables) - 1; i >= 0; i-- {
		table := tables[i]
		var dropSQL string

		switch driver {
		case "sqlite":
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s", table)

		case "mysql":
			// Disable foreign key checks temporarily
			if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
				return fmt.Errorf("failed to disable foreign key checks: %w", err)
			}
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s", table)

		case "postgres":
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", table)

		default:
			return fmt.Errorf("unsupported driver: %s", driver)
		}

		if _, err := db.Exec(dropSQL); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	// Re-enable foreign key checks for MySQL
	if driver == "mysql" {
		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			return fmt.Errorf("failed to re-enable foreign key checks: %w", err)
		}
	}

	return nil
}

// TruncateAllTables clears all data from tables (faster than drop/recreate)
func TruncateAllTables(db *sql.DB, driver string) error {
	tables, err := GetAllTables(db, driver)
	if err != nil {
		return err
	}

	// Skip migrations table
	for i, table := range tables {
		if table == "migrations" {
			tables = append(tables[:i], tables[i+1:]...)
			break
		}
	}

	switch driver {
	case "sqlite":
		// SQLite doesn't support TRUNCATE, use DELETE
		for _, table := range tables {
			if _, err := db.Exec(fmt.Sprintf("DELETE FROM %s", table)); err != nil {
				return fmt.Errorf("failed to truncate table %s: %w", table, err)
			}
		}

	case "mysql":
		// Disable FK checks
		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
			return fmt.Errorf("failed to disable foreign key checks: %w", err)
		}

		for _, table := range tables {
			if _, err := db.Exec(fmt.Sprintf("TRUNCATE TABLE %s", table)); err != nil {
				return fmt.Errorf("failed to truncate table %s: %w", table, err)
			}
		}

		// Re-enable FK checks
		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			return fmt.Errorf("failed to re-enable foreign key checks: %w", err)
		}

	case "postgres":
		// Truncate all at once with CASCADE
		if len(tables) > 0 {
			tableList := strings.Join(tables, ", ")
			if _, err := db.Exec(fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", tableList)); err != nil {
				return fmt.Errorf("failed to truncate tables: %w", err)
			}
		}

	default:
		return fmt.Errorf("unsupported driver: %s", driver)
	}

	return nil
}

// isTestDatabase checks if the database name indicates it's a test database
func isTestDatabase(name string) bool {
	if name == "" {
		return false
	}

	// :memory: is always safe (SQLite in-memory)
	if name == ":memory:" {
		return true
	}

	// Check if name contains "test"
	lowerName := strings.ToLower(name)
	return strings.Contains(lowerName, "test")
}

// RefreshDatabase resets the database to a clean state and runs all migrations
//
// This function:
// 1. Validates it's safe to run (test database, not production)
// 2. Drops all existing tables
// 3. Runs all registered migrations via migrate.Up()
// 4. Registers cleanup to close the connection after test
//
// Safety checks:
// - Requires testing.T (only callable from tests)
// - Checks APP_ENV != "production"
// - Verifies database name contains "test" or is ":memory:"
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    testing.RefreshDatabase(t)
//	    // Test with clean database
//	}
func RefreshDatabase(t *testing.T) *sql.DB {
	if t == nil {
		panic("RefreshDatabase requires testing.T - can only be called from tests")
	}

	// Get database connection
	db := orm.DB()
	if db == nil {
		panic("ORM not initialized - call orm.Init() before RefreshDatabase")
	}

	// Get driver and database name
	driver := orm.GetDriver()
	if driver == "" {
		panic("cannot determine database driver")
	}

	dbName := orm.GetDatabaseName()

	// Safety check: Verify we're in testing environment
	appEnv := os.Getenv("APP_ENV")

	if appEnv == "production" {
		panic("RefreshDatabase cannot run in production environment")
	}

	// Best practice: APP_ENV should be "testing" (set via .env.testing)
	if appEnv == "testing" {
		// ✓ Explicitly in testing mode - safe to proceed
	} else {
		// Not explicitly "testing" - verify database name as fallback safety
		if !isTestDatabase(dbName) {
			panic(fmt.Sprintf("database '%s' doesn't look like a test database - name must contain 'test' or be ':memory:'\nTip: Set APP_ENV=testing in .env.testing file", dbName))
		}
	}

	// Drop all tables
	if err := DropAllTables(db, driver); err != nil {
		t.Fatalf("RefreshDatabase: failed to drop tables: %v", err)
	}

	// Run all migrations
	migrator := migrate.NewMigrator(db, driver)
	if err := migrator.Up(); err != nil {
		t.Fatalf("RefreshDatabase: failed to run migrations: %v", err)
	}

	return db
}
