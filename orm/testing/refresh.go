package testing

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// dbIdentifierRegex validates database/table names
var dbIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// quoteIdentifier quotes a table/database name for use in DDL statements
func quoteIdentifier(name, driver string) string {
	if !dbIdentifierRegex.MatchString(name) {
		panic(fmt.Sprintf("invalid identifier: %q", name))
	}
	if driver == "mysql" {
		return "`" + name + "`"
	}
	return `"` + name + `"`
}

// GetAllTables returns a list of all tables in the database.
// For MySQL, the dbName parameter is used for the information_schema query.
func GetAllTables(db *sql.DB, driver string, dbName ...string) ([]string, error) {
	var query string

	switch driver {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name"

	case "mysql":
		name := ""
		if len(dbName) > 0 {
			name = dbName[0]
		}
		if name == "" {
			return nil, fmt.Errorf("cannot get database name for MySQL")
		}
		if !dbIdentifierRegex.MatchString(name) {
			return nil, fmt.Errorf("invalid database name: %q", name)
		}
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = ? ORDER BY table_name"
		rows, err := db.Query(query, name)
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
func DropAllTables(db *sql.DB, driver string, dbName ...string) error {
	tables, err := GetAllTables(db, driver, dbName...)
	if err != nil {
		return err
	}

	// Drop in reverse order to handle foreign key constraints
	for i := len(tables) - 1; i >= 0; i-- {
		table := tables[i]
		var dropSQL string

		quoted := quoteIdentifier(table, driver)

		switch driver {
		case "sqlite":
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s", quoted)

		case "mysql":
			// Disable foreign key checks temporarily
			if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
				return fmt.Errorf("failed to disable foreign key checks: %w", err)
			}
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s", quoted)

		case "postgres":
			dropSQL = fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", quoted)

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
func TruncateAllTables(db *sql.DB, driver string, dbName ...string) error {
	tables, err := GetAllTables(db, driver, dbName...)
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
			quoted := quoteIdentifier(table, driver)
			if _, err := db.Exec(fmt.Sprintf("DELETE FROM %s", quoted)); err != nil {
				return fmt.Errorf("failed to truncate table %s: %w", table, err)
			}
		}

	case "mysql":
		// Disable FK checks
		if _, err := db.Exec("SET FOREIGN_KEY_CHECKS = 0"); err != nil {
			return fmt.Errorf("failed to disable foreign key checks: %w", err)
		}

		for _, table := range tables {
			quoted := quoteIdentifier(table, driver)
			if _, err := db.Exec(fmt.Sprintf("TRUNCATE TABLE %s", quoted)); err != nil {
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
			quotedTables := make([]string, len(tables))
			for i, table := range tables {
				quotedTables[i] = quoteIdentifier(table, driver)
			}
			tableList := strings.Join(quotedTables, ", ")
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

// RefreshDatabase resets the database to a clean state and runs all migrations.
//
// This function:
// 1. Validates it's safe to run (test database, not production)
// 2. Drops all existing tables
// 3. Runs all registered migrations via migrate.Up()
//
// Safety checks:
// - Requires testing.T (only callable from tests)
// - Checks APP_ENV != "production"
// - Verifies database name contains "test" or is ":memory:"
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    testing.RefreshDatabase(t, manager)
//	    // Test with clean database
//	}
func RefreshDatabase(t *testing.T, manager *orm.Manager) *sql.DB {
	if t == nil {
		panic("RefreshDatabase requires testing.T - can only be called from tests")
	}

	// Get database connection
	db := manager.DB()
	if db == nil {
		panic("ORM not connected - manager has no active database connection")
	}

	// Get driver and database name
	driver := manager.DriverName()
	if driver == "" {
		panic("cannot determine database driver")
	}

	dbName := manager.DatabaseName()

	// Safety check: Verify we're in testing environment
	appEnv := os.Getenv("APP_ENV")

	if appEnv == "production" {
		panic("RefreshDatabase cannot run in production environment")
	}

	// Best practice: APP_ENV should be "testing" (set via .env.testing)
	if appEnv == "testing" {
		// Explicitly in testing mode - safe to proceed
	} else {
		// Not explicitly "testing" - verify database name as fallback safety
		if !isTestDatabase(dbName) {
			panic(fmt.Sprintf("database '%s' doesn't look like a test database - name must contain 'test' or be ':memory:'\nTip: Set APP_ENV=testing in .env.testing file", dbName))
		}
	}

	// Drop all tables
	if err := DropAllTables(db, driver, dbName); err != nil {
		t.Fatalf("RefreshDatabase: failed to drop tables: %v", err)
	}

	// Run all migrations
	migrator := migrate.NewMigrator(db, driver)
	if err := migrator.Up(); err != nil {
		t.Fatalf("RefreshDatabase: failed to run migrations: %v", err)
	}

	return db
}
