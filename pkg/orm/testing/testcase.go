package testing

import (
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
)

var (
	schemaRefreshed     bool
	schemaRefreshedOnce sync.Once
)

// TestCase provides test helpers for database testing
type TestCase struct {
	t  *testing.T
	db *sql.DB
}

// NewTestCase creates a new test case instance
func NewTestCase(t *testing.T) *TestCase {
	return &TestCase{
		t:  t,
		db: orm.DB(),
	}
}

// LazyRefreshDatabase resets the database for testing:
// 1. Runs migrations ONCE per test suite (not per test)
// 2. Truncates all tables before each test (fast cleanup)
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t)
//	    tc.LazyRefreshDatabase()
//
//	    // Test code - clean database, fast setup
//	}
func (tc *TestCase) LazyRefreshDatabase() {
	tc.ensureSafeEnvironment()

	// Run migrations ONCE per test suite
	schemaRefreshedOnce.Do(func() {
		// Drop all tables and run migrations fresh
		if err := DropAllTables(tc.db, orm.GetDriver()); err != nil {
			tc.t.Fatalf("LazyRefreshDatabase: failed to drop tables: %v", err)
		}

		migrator := migrate.NewMigrator(tc.db, orm.GetDriver())
		if err := migrator.Up(); err != nil {
			tc.t.Fatalf("LazyRefreshDatabase: failed to run migrations: %v", err)
		}
		schemaRefreshed = true
	})

	// Truncate tables for each test (fast cleanup)
	if err := TruncateAllTables(tc.db, orm.GetDriver()); err != nil {
		tc.t.Fatalf("LazyRefreshDatabase: failed to truncate tables: %v", err)
	}
}

// RefreshDatabase drops all tables and runs migrations for EACH test
// This is slower but more thorough - use when you need true isolation
// (e.g., testing migrations themselves)
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t)
//	    tc.RefreshDatabase()
//
//	    // Test code - completely fresh database
//	}
func (tc *TestCase) RefreshDatabase() {
	tc.ensureSafeEnvironment()

	// Drop all tables
	if err := DropAllTables(tc.db, orm.GetDriver()); err != nil {
		tc.t.Fatalf("RefreshDatabase: failed to drop tables: %v", err)
	}

	// Run migrations
	migrator := migrate.NewMigrator(tc.db, orm.GetDriver())
	if err := migrator.Up(); err != nil {
		tc.t.Fatalf("RefreshDatabase: failed to run migrations: %v", err)
	}
}

// DB returns the database connection
func (tc *TestCase) DB() *sql.DB {
	return tc.db
}

// ensureSafeEnvironment checks we're in test mode
func (tc *TestCase) ensureSafeEnvironment() {
	appEnv := os.Getenv("APP_ENV")

	if appEnv == "production" {
		panic("Tests cannot run in production environment")
	}

	if appEnv != "testing" {
		dbName := orm.GetDatabaseName()
		if !isTestDatabase(dbName) {
			panic("Not in testing environment and database doesn't look like a test database.\nTip: Set APP_ENV=testing in .env.testing")
		}
	}
}

// ResetSchemaRefresh resets the schema refresh flag (for testing the testing framework)
func ResetSchemaRefresh() {
	schemaRefreshed = false
	schemaRefreshedOnce = sync.Once{}
}
