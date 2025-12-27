package orm

import (
	"testing"
	"time"
)

// TestUser model for testing
type TestUser struct {
	Model[TestUser]
	Name     string `orm:"column:name"`
	Email    string `orm:"column:email;unique"`
	Age      int    `orm:"column:age"`
	IsActive bool   `orm:"column:is_active"`
}

func (TestUser) TableName() string {
	return "test_users"
}

func TestModelSave(t *testing.T) {
	// Initialize with SQLite in-memory database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Create the test table
	_, err = DB().Exec(`
		CREATE TABLE test_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT UNIQUE NOT NULL,
			age INTEGER,
			is_active BOOLEAN,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Test inserting a new user
	user := &TestUser{
		Name:     "John Doe",
		Email:    "john@example.com",
		Age:      30,
		IsActive: true,
	}

	// Save the user
	err = Save(user)
	if err != nil {
		t.Fatalf("Failed to save user: %v", err)
	}

	// Check that ID was set
	if user.Model.ID == 0 {
		t.Error("Expected user ID to be set after save")
	}

	// Check that timestamps were set
	if user.Model.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt to be set after save")
	}
	if user.Model.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set after save")
	}

	// Check that exists flag was set
	if !user.Model.IsExisting {
		t.Error("Expected IsExisting flag to be true after save")
	}

	// Verify the user was actually inserted
	var count int
	err = DB().QueryRow("SELECT COUNT(*) FROM test_users WHERE email = ?", user.Email).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query user count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 user, got %d", count)
	}

	// Test updating an existing user
	originalUpdatedAt := user.Model.UpdatedAt
	time.Sleep(10 * time.Millisecond) // Ensure time difference

	user.Name = "Jane Doe"
	user.Age = 31

	err = Save(user)
	if err != nil {
		t.Fatalf("Failed to update user: %v", err)
	}

	// Check that UpdatedAt changed
	if !user.Model.UpdatedAt.After(originalUpdatedAt) {
		t.Error("Expected UpdatedAt to be updated after save")
	}

	// Verify the update
	var name string
	var age int
	err = DB().QueryRow("SELECT name, age FROM test_users WHERE id = ?", user.Model.ID).Scan(&name, &age)
	if err != nil {
		t.Fatalf("Failed to query updated user: %v", err)
	}
	if name != "Jane Doe" {
		t.Errorf("Expected name to be 'Jane Doe', got '%s'", name)
	}
	if age != 31 {
		t.Errorf("Expected age to be 31, got %d", age)
	}
}

func TestModelCreate(t *testing.T) {
	// Initialize with SQLite in-memory database
	err := Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer Close()

	// Create the test table
	_, err = DB().Exec(`
		CREATE TABLE test_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT UNIQUE NOT NULL,
			age INTEGER,
			is_active BOOLEAN,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Test Create with map - should return created model
	user, err := TestUser{}.Create(map[string]any{
		"name":      "Alice",
		"email":     "alice@example.com",
		"age":       25,
		"is_active": true,
	})
	if err != nil {
		t.Fatalf("Failed to create user from map: %v", err)
	}

	// Check that user was returned with ID
	if user == nil {
		t.Fatal("Expected user to be returned, got nil")
	}
	if user.Model.ID == 0 {
		t.Error("Expected user ID to be set after create")
	}
	if user.Name != "Alice" {
		t.Errorf("Expected user name to be 'Alice', got '%s'", user.Name)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Expected user email to be 'alice@example.com', got '%s'", user.Email)
	}

	// Verify the user was created in database
	var count int
	err = DB().QueryRow("SELECT COUNT(*) FROM test_users WHERE email = ?", "alice@example.com").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query user count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 user, got %d", count)
	}
}

func TestStructToMap(t *testing.T) {
	user := &TestUser{
		Name:     "Bob",
		Email:    "bob@example.com",
		Age:      28,
		IsActive: false,
	}
	user.Model.ID = 123
	user.Model.CreatedAt = time.Now()
	user.Model.UpdatedAt = time.Now()

	data := structToMap(user)

	// Check that fields are properly mapped
	if data["name"] != "Bob" {
		t.Errorf("Expected name to be 'Bob', got %v", data["name"])
	}
	if data["email"] != "bob@example.com" {
		t.Errorf("Expected email to be 'bob@example.com', got %v", data["email"])
	}
	if data["age"] != 28 {
		t.Errorf("Expected age to be 28, got %v", data["age"])
	}
	if data["is_active"] != false {
		t.Errorf("Expected is_active to be false, got %v", data["is_active"])
	}

	// Check that timestamps are included
	if _, ok := data["created_at"]; !ok {
		t.Error("Expected created_at to be in map")
	}
	if _, ok := data["updated_at"]; !ok {
		t.Error("Expected updated_at to be in map")
	}
}
