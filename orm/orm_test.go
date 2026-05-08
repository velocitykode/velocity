package orm

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Test model
type User struct {
	Model[User]
	Name  string `orm:"column:name"`
	Email string `orm:"column:email;unique"`
	Age   int    `orm:"column:age"`
}

func (User) TableName() string {
	return "users"
}

func newTestManager(t testing.TB) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	return m
}

func TestNewManager(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	// Test connection
	if err := m.Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}
}

func TestDriverCreation(t *testing.T) {
	// Verify that known drivers are registered. The registry holds factories,
	// not constructed drivers, so the assertion is on Has rather than calling
	// the factory (which would require a working database connection for
	// postgres / mysql).
	for _, name := range []string{"sqlite", "sqlite3", "postgres", "mysql"} {
		if !Drivers().Has(name) {
			t.Errorf("Expected built-in driver %q to be registered", name)
		}
	}

	if Drivers().Has("unknown") {
		t.Error("Expected unknown driver to NOT be registered")
	}
}

func TestConnectionPool(t *testing.T) {
	m, err := NewManager(ManagerConfig{
		Driver:          "sqlite",
		Database:        ":memory:",
		MaxIdleConns:    5,
		MaxOpenConns:    10,
		ConnMaxLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer m.Shutdown(context.Background())

	// Get stats
	stats := m.Stats()
	if stats.MaxOpenConnections != 10 {
		t.Errorf("Expected MaxOpenConnections to be 10, got %d", stats.MaxOpenConnections)
	}
}

func TestTransaction(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	if _, err := m.Exec(context.Background(), `CREATE TABLE widgets (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	countWidgets := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := m.DB().QueryRow(`SELECT COUNT(*) FROM widgets`).Scan(&n); err != nil {
			t.Fatalf("count widgets: %v", err)
		}
		return n
	}

	// txFromCtx asserts and returns the raw *sql.Tx attached to ctx by
	// Manager.Transaction. Used here to keep these tests at the raw-SQL
	// level (the ORM helpers have their own dedicated tests in
	// tx_context_test.go).
	txFromCtx := func(t *testing.T, ctx context.Context) *sql.Tx {
		t.Helper()
		tx, ok := TxFromContext(ctx)
		if !ok || tx == nil {
			t.Fatal("TxFromContext returned no tx; Manager.Transaction did not enroll ctx")
		}
		return tx
	}

	t.Run("commit persists writes", func(t *testing.T) {
		if _, err := m.Exec(context.Background(), `DELETE FROM widgets`); err != nil {
			t.Fatalf("reset: %v", err)
		}
		err := m.Transaction(context.Background(), func(ctx context.Context) error {
			tx := txFromCtx(t, ctx)
			_, err := tx.Exec(`INSERT INTO widgets (name) VALUES (?)`, "kept")
			return err
		})
		if err != nil {
			t.Fatalf("Transaction returned error: %v", err)
		}
		if got := countWidgets(t); got != 1 {
			t.Errorf("post-commit row count = %d, want 1", got)
		}
	})

	t.Run("callback error rolls back writes", func(t *testing.T) {
		if _, err := m.Exec(context.Background(), `DELETE FROM widgets`); err != nil {
			t.Fatalf("reset: %v", err)
		}
		err := m.Transaction(context.Background(), func(ctx context.Context) error {
			tx := txFromCtx(t, ctx)
			if _, err := tx.Exec(`INSERT INTO widgets (name) VALUES (?)`, "ghost"); err != nil {
				return err
			}
			return ErrTransaction // trigger rollback
		})
		if err != ErrTransaction {
			t.Errorf("Transaction returned %v, want ErrTransaction", err)
		}
		// The real assertion: nothing survived. A mutation that swallows
		// the callback error (e.g. `if err != nil { err = nil }`) would
		// commit the ghost row and this count would be 1.
		if got := countWidgets(t); got != 0 {
			t.Errorf("post-rollback row count = %d, want 0", got)
		}
	})

	t.Run("panic also rolls back", func(t *testing.T) {
		if _, err := m.Exec(context.Background(), `DELETE FROM widgets`); err != nil {
			t.Fatalf("reset: %v", err)
		}
		func() {
			defer func() { _ = recover() }()
			_ = m.Transaction(context.Background(), func(ctx context.Context) error {
				tx := txFromCtx(t, ctx)
				if _, err := tx.Exec(`INSERT INTO widgets (name) VALUES (?)`, "panicked"); err != nil {
					t.Fatalf("insert: %v", err)
				}
				panic("boom")
			})
		}()
		if got := countWidgets(t); got != 0 {
			t.Errorf("post-panic row count = %d, want 0 (panic must rollback)", got)
		}
	})
}

func TestManagerExec(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	// Create users table
	_, err := m.Exec(context.Background(), `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT UNIQUE NOT NULL,
		age INTEGER,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	// Insert test data
	_, err = m.Exec(context.Background(), `INSERT INTO users (name, email, age, created_at, updated_at) VALUES
		('Alice', 'alice@example.com', 25, datetime('now'), datetime('now')),
		('Bob', 'bob@example.com', 30, datetime('now'), datetime('now')),
		('Charlie', 'charlie@example.com', 35, datetime('now'), datetime('now'))
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Verify data via Raw query
	rows, err := m.Raw(context.Background(), "SELECT COUNT(*) FROM users WHERE age > ?", 25)
	if err != nil {
		t.Fatalf("Raw query failed: %v", err)
	}
	defer rows.Close()

	var count int
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			t.Fatalf("Scan failed: %v", err)
		}
	}
	if count != 2 {
		t.Errorf("Expected 2 users with age > 25, got %d", count)
	}
}

func TestManagerDriverName(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	name := m.DriverName()
	if name != "sqlite3" && name != "sqlite" {
		t.Errorf("Expected sqlite driver name, got %q", name)
	}
}

func TestManagerDatabaseName(t *testing.T) {
	m := newTestManager(t)
	defer m.Shutdown(context.Background())

	dbName := m.DatabaseName()
	if dbName != ":memory:" {
		t.Errorf("Expected :memory:, got %q", dbName)
	}
}

func BenchmarkManagerExec(b *testing.B) {
	m := newTestManager(b)
	defer m.Shutdown(context.Background())

	m.Exec(context.Background(), `CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT,
		email TEXT,
		age INTEGER
	)`)

	for i := 0; i < 100; i++ {
		m.Exec(context.Background(), "INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
			"User", "user@example.com", 25)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.Raw(context.Background(), "SELECT * FROM users WHERE age > ? ORDER BY id DESC LIMIT 10", 20)
	}
}
