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
	tx *sql.Tx
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
// 2. Wraps each test in a transaction
// 3. Rolls back after test (fast, ~1ms)
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t)
//	    tc.LazyRefreshDatabase()
//
//	    // Test code - runs in transaction, auto-rollback after
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

	// Begin transaction for this test
	tc.beginTransaction()
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

// beginTransaction starts a transaction and sets up rollback on cleanup
func (tc *TestCase) beginTransaction() {
	tx, err := tc.db.Begin()
	if err != nil {
		tc.t.Fatalf("failed to begin transaction: %v", err)
	}
	tc.tx = tx

	// Override ORM's DB connection to use this transaction
	orm.SetTx(tx)

	// Rollback when test completes
	tc.t.Cleanup(func() {
		if tc.tx != nil {
			tc.tx.Rollback()
			orm.ClearTx()
		}
	})
}

// DB returns the database connection (or transaction if active)
func (tc *TestCase) DB() *sql.DB {
	return tc.db
}

// Tx returns the current transaction (nil if not in transaction)
func (tc *TestCase) Tx() *sql.Tx {
	return tc.tx
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
