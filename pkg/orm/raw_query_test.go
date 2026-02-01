package orm

import (
	"os"
	"testing"
)

// RawQueryUser is a custom struct for raw query results
type RawQueryUser struct {
	ID    uint   `orm:"column:id"`
	Name  string `orm:"column:name"`
	Email string `orm:"column:email"`
}

// RawQueryUserPartial is a partial struct for raw query results
type RawQueryUserPartial struct {
	Name  string `orm:"column:name"`
	Email string `orm:"column:email"`
}

func setupRawQueryTestDB(t *testing.T, driver string) func() {
	var config map[string]any

	switch driver {
	case "sqlite":
		config = map[string]any{
			"database": ":memory:",
		}
	case "postgres":
		// Use environment variables for PostgreSQL connection
		host := os.Getenv("POSTGRES_HOST")
		if host == "" {
			host = "localhost"
		}
		port := os.Getenv("POSTGRES_PORT")
		if port == "" {
			port = "5432"
		}
		database := os.Getenv("POSTGRES_DB")
		if database == "" {
			database = "velocity_test"
		}
		username := os.Getenv("POSTGRES_USER")
		if username == "" {
			username = "postgres"
		}
		password := os.Getenv("POSTGRES_PASSWORD")
		if password == "" {
			password = ""
		}

		config = map[string]any{
			"host":     host,
			"port":     port,
			"database": database,
			"username": username,
			"password": password,
		}
	default:
		t.Fatalf("Unsupported driver: %s", driver)
	}

	err := Init(driver, config)
	if err != nil {
		if driver == "postgres" {
			t.Skipf("Skipping PostgreSQL test: %v", err)
		}
		t.Fatalf("Failed to initialize ORM with %s: %v", driver, err)
	}

	// Create test table
	var createSQL string
	if driver == "sqlite" {
		createSQL = `
			CREATE TABLE IF NOT EXISTS raw_query_users (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				name TEXT NOT NULL,
				email TEXT NOT NULL,
				age INTEGER,
				created_at DATETIME,
				updated_at DATETIME
			)
		`
	} else {
		createSQL = `
			CREATE TABLE IF NOT EXISTS raw_query_users (
				id SERIAL PRIMARY KEY,
				name TEXT NOT NULL,
				email TEXT NOT NULL,
				age INTEGER,
				created_at TIMESTAMP,
				updated_at TIMESTAMP
			)
		`
	}

	_, err = DB().Exec(createSQL)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Clean up any existing data
	_, _ = DB().Exec("DELETE FROM raw_query_users")

	// Insert test data
	if driver == "sqlite" {
		_, err = DB().Exec(`
			INSERT INTO raw_query_users (name, email, age) VALUES
			('Alice', 'alice@example.com', 25),
			('Bob', 'bob@example.com', 30),
			('Charlie', 'charlie@example.com', 35)
		`)
	} else {
		_, err = DB().Exec(`
			INSERT INTO raw_query_users (name, email, age) VALUES
			('Alice', 'alice@example.com', 25),
			('Bob', 'bob@example.com', 30),
			('Charlie', 'charlie@example.com', 35)
		`)
	}
	if err != nil {
		t.Fatalf("Failed to insert test data: %v", err)
	}

	return func() {
		_, _ = DB().Exec("DROP TABLE IF EXISTS raw_query_users")
		Close()
	}
}

func TestRawQuery_First_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	var user RawQueryUser
	err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users WHERE name = ?", "Alice").First(&user)
	if err != nil {
		t.Fatalf("RawQuery.First() failed: %v", err)
	}

	if user.Name != "Alice" {
		t.Errorf("Expected name 'Alice', got '%s'", user.Name)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Expected email 'alice@example.com', got '%s'", user.Email)
	}
}

func TestRawQuery_First_NotFound_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	var user RawQueryUser
	err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users WHERE name = ?", "NonExistent").First(&user)
	if err != ErrRecordNotFound {
		t.Errorf("Expected ErrRecordNotFound, got %v", err)
	}
}

func TestRawQuery_Get_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	users, err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users ORDER BY name").Get()
	if err != nil {
		t.Fatalf("RawQuery.Get() failed: %v", err)
	}

	if len(users) != 3 {
		t.Fatalf("Expected 3 users, got %d", len(users))
	}

	expectedNames := []string{"Alice", "Bob", "Charlie"}
	for i, user := range users {
		if user.Name != expectedNames[i] {
			t.Errorf("Expected user[%d].Name = '%s', got '%s'", i, expectedNames[i], user.Name)
		}
	}
}

func TestRawQuery_Get_Empty_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	users, err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users WHERE age > ?", 100).Get()
	if err != nil {
		t.Fatalf("RawQuery.Get() failed: %v", err)
	}

	if len(users) != 0 {
		t.Errorf("Expected 0 users, got %d", len(users))
	}
}

func TestRawQuery_Scan_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	var count int
	err := NewRawQuery[RawQueryUser]("SELECT COUNT(*) FROM raw_query_users").Scan(&count)
	if err != nil {
		t.Fatalf("RawQuery.Scan() failed: %v", err)
	}

	if count != 3 {
		t.Errorf("Expected count 3, got %d", count)
	}
}

func TestRawQuery_Exec_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	affected, err := NewRawQuery[RawQueryUser]("UPDATE raw_query_users SET age = ? WHERE name = ?", 26, "Alice").Exec()
	if err != nil {
		t.Fatalf("RawQuery.Exec() failed: %v", err)
	}

	if affected != 1 {
		t.Errorf("Expected 1 row affected, got %d", affected)
	}

	// Verify the update
	var age int
	err = DB().QueryRow("SELECT age FROM raw_query_users WHERE name = ?", "Alice").Scan(&age)
	if err != nil {
		t.Fatalf("Failed to verify update: %v", err)
	}
	if age != 26 {
		t.Errorf("Expected age 26, got %d", age)
	}
}

func TestRawQuery_PartialStruct_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	// Test with partial struct (no ID field)
	var user RawQueryUserPartial
	err := NewRawQuery[RawQueryUserPartial]("SELECT name, email FROM raw_query_users WHERE name = ?", "Bob").First(&user)
	if err != nil {
		t.Fatalf("RawQuery.First() with partial struct failed: %v", err)
	}

	if user.Name != "Bob" {
		t.Errorf("Expected name 'Bob', got '%s'", user.Name)
	}
	if user.Email != "bob@example.com" {
		t.Errorf("Expected email 'bob@example.com', got '%s'", user.Email)
	}
}

func TestRawQuery_WithPlaceholders_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	users, err := NewRawQuery[RawQueryUser](
		"SELECT id, name, email FROM raw_query_users WHERE age >= ? AND age <= ? ORDER BY age",
		25, 30,
	).Get()
	if err != nil {
		t.Fatalf("RawQuery.Get() with multiple placeholders failed: %v", err)
	}

	if len(users) != 2 {
		t.Fatalf("Expected 2 users, got %d", len(users))
	}
}

// PostgreSQL tests - these require a running PostgreSQL instance
func TestRawQuery_First_Postgres(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") == "" {
		t.Skip("Skipping PostgreSQL test (set TEST_POSTGRES=1 to run)")
	}

	cleanup := setupRawQueryTestDB(t, "postgres")
	defer cleanup()

	var user RawQueryUser
	err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users WHERE name = $1", "Alice").First(&user)
	if err != nil {
		t.Fatalf("RawQuery.First() failed: %v", err)
	}

	if user.Name != "Alice" {
		t.Errorf("Expected name 'Alice', got '%s'", user.Name)
	}
}

func TestRawQuery_Get_Postgres(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") == "" {
		t.Skip("Skipping PostgreSQL test (set TEST_POSTGRES=1 to run)")
	}

	cleanup := setupRawQueryTestDB(t, "postgres")
	defer cleanup()

	users, err := NewRawQuery[RawQueryUser]("SELECT id, name, email FROM raw_query_users ORDER BY name").Get()
	if err != nil {
		t.Fatalf("RawQuery.Get() failed: %v", err)
	}

	if len(users) != 3 {
		t.Fatalf("Expected 3 users, got %d", len(users))
	}
}

func TestRawQuery_Scan_Postgres(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") == "" {
		t.Skip("Skipping PostgreSQL test (set TEST_POSTGRES=1 to run)")
	}

	cleanup := setupRawQueryTestDB(t, "postgres")
	defer cleanup()

	var count int
	err := NewRawQuery[RawQueryUser]("SELECT COUNT(*) FROM raw_query_users").Scan(&count)
	if err != nil {
		t.Fatalf("RawQuery.Scan() failed: %v", err)
	}

	if count != 3 {
		t.Errorf("Expected count 3, got %d", count)
	}
}

// TestModel_Raw tests the Raw method on Model types
func TestModel_Raw_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	// Test using Model{}.Raw()
	var user RawQueryUser
	err := Model[RawQueryUser]{}.Raw("SELECT id, name, email FROM raw_query_users WHERE name = ?", "Charlie").First(&user)
	if err != nil {
		t.Fatalf("Model.Raw().First() failed: %v", err)
	}

	if user.Name != "Charlie" {
		t.Errorf("Expected name 'Charlie', got '%s'", user.Name)
	}
}

// TestModel_Raw_Get tests the Raw method with Get()
func TestModel_Raw_Get_SQLite(t *testing.T) {
	cleanup := setupRawQueryTestDB(t, "sqlite")
	defer cleanup()

	users, err := Model[RawQueryUser]{}.Raw("SELECT id, name, email FROM raw_query_users WHERE age < ?", 35).Get()
	if err != nil {
		t.Fatalf("Model.Raw().Get() failed: %v", err)
	}

	if len(users) != 2 {
		t.Errorf("Expected 2 users, got %d", len(users))
	}
}
