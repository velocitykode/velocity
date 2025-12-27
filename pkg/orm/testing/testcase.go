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
	migrationRun     bool
	migrationRunOnce sync.Once
)

// TestCase provides test helpers
type TestCase struct {
	t  *testing.T
	db *sql.DB
	tx *sql.Tx
}

// NewTestCase creates a new test case instance
func NewTestCase(t *testing.T) *TestCase {
	return &TestCase{
		t:  t,
		db: orm.DB(),
	}
}

// LazyRefreshDatabase drops tables and runs migrations before each test
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t)
//	    tc.LazyRefreshDatabase()
//
//	    // Test code - fresh database
//	}
func (tc *TestCase) LazyRefreshDatabase() {
	// Safety checks
	tc.ensureSafeEnvironment()

	// Drop all tables
	if err := DropAllTables(tc.db, orm.GetDriver()); err != nil {
		tc.t.Fatalf("LazyRefreshDatabase: failed to drop tables: %v", err)
	}

	// Run migrations
	migrator := migrate.NewMigrator(tc.db, orm.GetDriver())
	if err := migrator.Up(); err != nil {
		tc.t.Fatalf("LazyRefreshDatabase: failed to run migrations: %v", err)
	}
}

// RefreshDatabase drops all tables and runs migrations for EACH test
// This is slower but more thorough - use when you need true isolation
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t)
//	    tc.RefreshDatabase()
//
//	    // Test code - starts with completely fresh database
//	}
func (tc *TestCase) RefreshDatabase() {
	RefreshDatabase(tc.t)
	tc.db = orm.DB()
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

// Helper function for backwards compatibility
// You can still use the standalone function if you prefer
func lazyRefreshDatabase(t *testing.T) *sql.DB {
	tc := NewTestCase(t)
	tc.LazyRefreshDatabase()
	return tc.db
}
