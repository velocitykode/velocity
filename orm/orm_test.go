package orm

import (
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
	defer m.Close()

	// Test connection
	if err := m.Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}
}

func TestDriverCreation(t *testing.T) {
	// Verify that known drivers can be created
	for _, name := range []string{"sqlite", "postgres", "mysql"} {
		_, err := createDriver(name)
		if err != nil {
			t.Errorf("Failed to create driver %q: %v", name, err)
		}
	}

	// Unknown driver should error
	_, err := createDriver("unknown")
	if err == nil {
		t.Error("Expected error for unknown driver")
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
	defer m.Close()

	// Get stats
	stats := m.Stats()
	if stats.MaxOpenConnections != 10 {
		t.Errorf("Expected MaxOpenConnections to be 10, got %d", stats.MaxOpenConnections)
	}
}

func TestTransaction(t *testing.T) {
	m := newTestManager(t)
	defer m.Close()

	// Test transaction
	err := m.Transaction(func(tx *sql.Tx) error {
		return nil
	})
	if err != nil {
		t.Errorf("Transaction failed: %v", err)
	}

	// Test transaction rollback
	err = m.Transaction(func(tx *sql.Tx) error {
		return ErrTransaction
	})
	if err != ErrTransaction {
		t.Error("Expected transaction to rollback with error")
	}
}

func TestManagerExec(t *testing.T) {
	m := newTestManager(t)
	defer m.Close()

	// Create users table
	_, err := m.Exec(`CREATE TABLE users (
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
	_, err = m.Exec(`INSERT INTO users (name, email, age, created_at, updated_at) VALUES
		('Alice', 'alice@example.com', 25, datetime('now'), datetime('now')),
		('Bob', 'bob@example.com', 30, datetime('now'), datetime('now')),
		('Charlie', 'charlie@example.com', 35, datetime('now'), datetime('now'))
	`)
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	// Verify data via Raw query
	rows, err := m.Raw("SELECT COUNT(*) FROM users WHERE age > ?", 25)
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
	defer m.Close()

	name := m.DriverName()
	if name != "sqlite3" && name != "sqlite" {
		t.Errorf("Expected sqlite driver name, got %q", name)
	}
}

func TestManagerDatabaseName(t *testing.T) {
	m := newTestManager(t)
	defer m.Close()

	dbName := m.DatabaseName()
	if dbName != ":memory:" {
		t.Errorf("Expected :memory:, got %q", dbName)
	}
}

func BenchmarkManagerExec(b *testing.B) {
	m := newTestManager(b)
	defer m.Close()

	m.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT,
		email TEXT,
		age INTEGER
	)`)

	for i := 0; i < 100; i++ {
		m.Exec("INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
			"User", "user@example.com", 25)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		m.Raw("SELECT * FROM users WHERE age > ? ORDER BY id DESC LIMIT 10", 20)
	}
}
