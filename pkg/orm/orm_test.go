package orm

import (
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

func TestInit(t *testing.T) {
	// Initialize with SQLite in-memory database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})

	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}

	// Test connection
	if err := Ping(); err != nil {
		t.Fatalf("Failed to ping database: %v", err)
	}

	// Close connection
	if err := Close(); err != nil {
		t.Fatalf("Failed to close database: %v", err)
	}
}

func TestDriverRegistration(t *testing.T) {
	// Check if SQLite driver is registered
	if _, exists := driverFactories["sqlite"]; !exists {
		t.Error("SQLite driver not registered")
	}
}

func TestConnectionPool(t *testing.T) {
	// Initialize database
	err := Init("sqlite", map[string]any{
		"database":       ":memory:",
		"max_idle_conns": 5,
		"max_open_conns": 10,
	})

	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Test pool settings
	SetMaxIdleConns(5)
	SetMaxOpenConns(10)
	SetConnMaxLifetime(time.Hour)

	// Get stats
	stats := Stats()
	if stats.MaxOpenConnections != 10 {
		t.Errorf("Expected MaxOpenConnections to be 10, got %d", stats.MaxOpenConnections)
	}
}

func TestTransaction(t *testing.T) {
	// Initialize database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})

	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Test transaction
	err = Transaction(func() error {
		// Transaction operations would go here
		return nil
	})

	if err != nil {
		t.Errorf("Transaction failed: %v", err)
	}

	// Test transaction rollback
	err = Transaction(func() error {
		// Return error to trigger rollback
		return ErrTransaction
	})

	if err != ErrTransaction {
		t.Error("Expected transaction to rollback with error")
	}
}

func TestModelBasics(t *testing.T) {
	// Initialize database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})

	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Create users table
	_, err = Exec(`CREATE TABLE users (
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

	// Test Model methods
	t.Run("Count", func(t *testing.T) {
		count, err := User{}.Count()
		if err != nil {
			t.Errorf("Failed to count users: %v", err)
		}
		if count != 0 {
			t.Errorf("Expected 0 users, got %d", count)
		}
	})

	t.Run("Exists", func(t *testing.T) {
		exists := User{}.Exists()
		if exists {
			t.Error("Expected no users to exist")
		}
	})
}

func TestQuery(t *testing.T) {
	// Initialize database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})

	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Create test table
	_, err = Exec(`CREATE TABLE users (
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
	_, err = Exec(`INSERT INTO users (name, email, age, created_at, updated_at) VALUES
		('Alice', 'alice@example.com', 25, datetime('now'), datetime('now')),
		('Bob', 'bob@example.com', 30, datetime('now'), datetime('now')),
		('Charlie', 'charlie@example.com', 35, datetime('now'), datetime('now'))
	`)

	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	t.Run("Where", func(t *testing.T) {
		query := User{}.Where("age > ?", 25)
		if query == nil {
			t.Error("Expected query builder, got nil")
		}
	})

	t.Run("OrderBy", func(t *testing.T) {
		query := User{}.OrderBy("name", "ASC")
		if query == nil {
			t.Error("Expected query builder, got nil")
		}
	})

	t.Run("Limit", func(t *testing.T) {
		query := User{}.OrderBy("id", "ASC").Limit(2)
		if query == nil {
			t.Error("Expected query builder, got nil")
		}
	})
}

func TestAutoInit(t *testing.T) {
	// Set environment variables
	t.Setenv("DB_CONNECTION", "sqlite")
	t.Setenv("DB_DATABASE", ":memory:")
	t.Setenv("DB_LOG_QUERIES", "true")
	t.Setenv("DB_MAX_IDLE_CONNS", "5")
	t.Setenv("DB_MAX_OPEN_CONNS", "10")

	// Re-initialize from environment
	err := InitFromEnv()
	if err != nil {
		t.Fatalf("Failed to initialize from environment: %v", err)
	}

	// Test connection
	if err := Ping(); err != nil {
		t.Errorf("Failed to ping database after auto-init: %v", err)
	}

	Close()
}

func BenchmarkQuery(b *testing.B) {
	// Initialize database
	Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	defer Close()

	// Create table
	Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		name TEXT,
		email TEXT,
		age INTEGER
	)`)

	// Insert test data
	for i := 0; i < 100; i++ {
		Exec("INSERT INTO users (name, email, age) VALUES (?, ?, ?)",
			"User", "user@example.com", 25)
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		User{}.Where("age > ?", 20).OrderBy("id", "DESC").Limit(10)
	}
}
