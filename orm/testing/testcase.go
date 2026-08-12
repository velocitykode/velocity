package testing

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

var schemaRefreshedOnce sync.Once

// TestCase provides test helpers for database testing
type TestCase struct {
	t       *testing.T
	db      *sql.DB
	manager *orm.Manager
}

// NewTestCase creates a new test case instance.
// The manager must be an initialized *orm.Manager with an active connection.
func NewTestCase(t *testing.T, manager *orm.Manager) *TestCase {
	return &TestCase{
		t:       t,
		db:      manager.DB(),
		manager: manager,
	}
}

// LazyRefreshDatabase resets the database for testing:
// 1. Runs migrations ONCE per test suite (not per test)
// 2. Truncates all tables before each test (fast cleanup)
//
// Usage:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t, manager)
//	    tc.LazyRefreshDatabase()
//
//	    // Test code - clean database, fast setup
//	}
func (tc *TestCase) LazyRefreshDatabase() {
	tc.ensureSafeEnvironment()

	driver := tc.manager.DriverName()

	// Run migrations ONCE per test suite
	schemaRefreshedOnce.Do(func() {
		// Drop all tables and run migrations fresh
		if err := DropAllTables(tc.db, driver); err != nil {
			tc.t.Fatalf("LazyRefreshDatabase: failed to drop tables: %v", err)
		}

		migrator := migrate.NewMigrator(tc.db, driver)
		if err := migrator.Up(); err != nil {
			tc.t.Fatalf("LazyRefreshDatabase: failed to run migrations: %v", err)
		}
	})

	// Truncate tables for each test (fast cleanup)
	if err := TruncateAllTables(tc.db, driver); err != nil {
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
//	    tc := testing.NewTestCase(t, manager)
//	    tc.RefreshDatabase()
//
//	    // Test code - completely fresh database
//	}
func (tc *TestCase) RefreshDatabase() {
	tc.ensureSafeEnvironment()

	driver := tc.manager.DriverName()

	// Drop all tables
	if err := DropAllTables(tc.db, driver); err != nil {
		tc.t.Fatalf("RefreshDatabase: failed to drop tables: %v", err)
	}

	// Run migrations
	migrator := migrate.NewMigrator(tc.db, driver)
	if err := migrator.Up(); err != nil {
		tc.t.Fatalf("RefreshDatabase: failed to run migrations: %v", err)
	}
}

// BeginTransaction provides transaction-rollback test isolation, the fast
// alternative to refreshing the whole database between tests:
//  1. Runs migrations ONCE per test suite (not per test).
//  2. Opens a transaction and returns a context carrying it. Every ORM call
//     (and every *Ctx assertion) that receives this context enrolls in the
//     transaction; nested Manager.Transaction calls become savepoints.
//  3. Registers a t.Cleanup that rolls the transaction back, so all writes the
//     test made vanish - no truncate, no re-migrate between tests.
//
// Pass the returned context to factories, ORM terminals, and the *Ctx
// assertions:
//
//	func TestExample(t *testing.T) {
//	    tc := testing.NewTestCase(t, manager)
//	    ctx := tc.BeginTransaction()
//
//	    _, _ = models.Order{}.Factory(manager).CreateMany(ctx, 5, nil)
//	    testing.AssertDatabaseCountCtx(t, ctx, manager, "orders", 5)
//	    // rolled back automatically at test end
//	}
//
// Reads that do not receive ctx hit the pool and will NOT see the transaction's
// uncommitted rows - always thread ctx (use the *Ctx assertions).
func (tc *TestCase) BeginTransaction() context.Context {
	tc.ensureSafeEnvironment()

	driver := tc.manager.DriverName()

	// Migrate once per suite (same guard as LazyRefreshDatabase).
	schemaRefreshedOnce.Do(func() {
		if err := DropAllTables(tc.db, driver); err != nil {
			tc.t.Fatalf("BeginTransaction: failed to drop tables: %v", err)
		}
		if err := migrate.NewMigrator(tc.db, driver).Up(); err != nil {
			tc.t.Fatalf("BeginTransaction: failed to run migrations: %v", err)
		}
	})

	tx, err := tc.manager.Begin(context.Background())
	if err != nil {
		tc.t.Fatalf("BeginTransaction: failed to begin transaction: %v", err)
	}
	tc.t.Cleanup(func() { _ = tx.Rollback() })

	return orm.WithTxContext(context.Background(), tx)
}

// DB returns the database connection
func (tc *TestCase) DB() *sql.DB {
	return tc.db
}

// ensureSafeEnvironment checks we're in test mode. Production-class APP_ENV
// values (production, prod, staging) panic outright; everything else requires
// either APP_ENV=testing/test or a database name that looks like a test fixture.
func (tc *TestCase) ensureSafeEnvironment() {
	appEnv := contract.GetEnv()

	if contract.IsProductionEnv(appEnv) {
		panic(fmt.Sprintf("Tests cannot run with APP_ENV=%q (production class)", appEnv))
	}

	if !contract.IsTestingEnv(appEnv) {
		dbName := tc.manager.DatabaseName()
		if !isTestDatabase(dbName) {
			panic("Not in testing environment and database doesn't look like a test database.\nTip: Set APP_ENV=testing in .env.testing")
		}
	}
}

// ResetSchemaRefresh resets the schema refresh flag (for testing the testing framework)
func ResetSchemaRefresh() {
	schemaRefreshedOnce = sync.Once{}
}
