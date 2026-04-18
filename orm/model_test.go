package orm

import (
	"context"
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
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()

	// Create the test table
	_, err := db.Exec(`
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
	err = Save(manager, user)
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
	err = db.QueryRow("SELECT COUNT(*) FROM test_users WHERE email = ?", user.Email).Scan(&count)
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

	err = Save(manager, user)
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
	err = db.QueryRow("SELECT name, age FROM test_users WHERE id = ?", user.Model.ID).Scan(&name, &age)
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
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()

	// Create the test table
	_, err := db.Exec(`
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

	// Set default manager so Create can resolve the driver
	SetDefault(manager)
	defer ResetDefault()

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
	err = db.QueryRow("SELECT COUNT(*) FROM test_users WHERE email = ?", "alice@example.com").Scan(&count)
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

// TestProject model for testing UUIDModel
type TestProject struct {
	UUIDModel[TestProject]
	Name        string `orm:"column:name"`
	Description string `orm:"column:description"`
}

func (TestProject) TableName() string {
	return "test_projects"
}

func TestUUIDModelSave(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()

	// Create the test table with UUID primary key
	_, err := db.Exec(`
		CREATE TABLE test_projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Test inserting a new project
	project := &TestProject{
		Name:        "My Project",
		Description: "A test project",
	}

	// Save the project
	err = Save(manager, project)
	if err != nil {
		t.Fatalf("Failed to save project: %v", err)
	}

	// Check that UUID was auto-generated
	if project.UUIDModel.ID == "" {
		t.Error("Expected project UUID to be auto-generated after save")
	}

	// Check UUID format (simple validation)
	if len(project.UUIDModel.ID) != 36 {
		t.Errorf("Expected UUID to be 36 characters, got %d", len(project.UUIDModel.ID))
	}

	// Check that timestamps were set
	if project.UUIDModel.CreatedAt.IsZero() {
		t.Error("Expected CreatedAt to be set after save")
	}
	if project.UUIDModel.UpdatedAt.IsZero() {
		t.Error("Expected UpdatedAt to be set after save")
	}

	// Check that exists flag was set
	if !project.UUIDModel.IsExisting {
		t.Error("Expected IsExisting flag to be true after save")
	}

	// Verify the project was actually inserted
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_projects WHERE id = ?", project.UUIDModel.ID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query project count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 project, got %d", count)
	}

	// Test updating an existing project
	originalUpdatedAt := project.UUIDModel.UpdatedAt
	time.Sleep(10 * time.Millisecond) // Ensure time difference

	project.Name = "Updated Project"
	project.Description = "Updated description"

	err = Save(manager, project)
	if err != nil {
		t.Fatalf("Failed to update project: %v", err)
	}

	// Check that UpdatedAt changed
	if !project.UUIDModel.UpdatedAt.After(originalUpdatedAt) {
		t.Error("Expected UpdatedAt to be updated after save")
	}

	// Verify the update
	var name, description string
	err = db.QueryRow("SELECT name, description FROM test_projects WHERE id = ?", project.UUIDModel.ID).Scan(&name, &description)
	if err != nil {
		t.Fatalf("Failed to query updated project: %v", err)
	}
	if name != "Updated Project" {
		t.Errorf("Expected name to be 'Updated Project', got '%s'", name)
	}
	if description != "Updated description" {
		t.Errorf("Expected description to be 'Updated description', got '%s'", description)
	}
}

func TestUUIDModelWithPresetID(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()

	// Create the test table
	_, err := db.Exec(`
		CREATE TABLE test_projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Test inserting with a preset UUID
	presetID := "550e8400-e29b-41d4-a716-446655440000"
	project := &TestProject{
		Name:        "Preset ID Project",
		Description: "Project with preset UUID",
	}
	project.UUIDModel.ID = presetID

	// Save the project
	err = Save(manager, project)
	if err != nil {
		t.Fatalf("Failed to save project with preset ID: %v", err)
	}

	// Check that the preset UUID was used (not overwritten)
	if project.UUIDModel.ID != presetID {
		t.Errorf("Expected UUID to remain %s, got %s", presetID, project.UUIDModel.ID)
	}

	// Verify it was inserted with the preset ID
	var retrievedName string
	err = db.QueryRow("SELECT name FROM test_projects WHERE id = ?", presetID).Scan(&retrievedName)
	if err != nil {
		t.Fatalf("Failed to query project by preset ID: %v", err)
	}
	if retrievedName != "Preset ID Project" {
		t.Errorf("Expected name to be 'Preset ID Project', got '%s'", retrievedName)
	}
}

func TestUUIDModelCreate(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()

	// Create the test table
	_, err := db.Exec(`
		CREATE TABLE test_projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	// Set default manager so Create can resolve the driver
	SetDefault(manager)
	defer ResetDefault()

	// Test Create with map - should return created model with auto-generated UUID
	project, err := TestProject{}.Create(map[string]any{
		"name":        "Created Project",
		"description": "Created via map",
	})
	if err != nil {
		t.Fatalf("Failed to create project from map: %v", err)
	}

	// Check that project was returned with UUID
	if project == nil {
		t.Fatal("Expected project to be returned, got nil")
	}
	if project.UUIDModel.ID == "" {
		t.Error("Expected project UUID to be auto-generated after create")
	}
	if project.Name != "Created Project" {
		t.Errorf("Expected project name to be 'Created Project', got '%s'", project.Name)
	}

	// Verify the project was created in database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM test_projects WHERE id = ?", project.UUIDModel.ID).Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query project count: %v", err)
	}
	if count != 1 {
		t.Errorf("Expected 1 project, got %d", count)
	}
}

func TestUUIDStructToMap(t *testing.T) {
	project := &TestProject{
		Name:        "Map Test Project",
		Description: "Testing structToMap for UUIDModel",
	}
	project.UUIDModel.ID = "test-uuid-123"
	project.UUIDModel.CreatedAt = time.Now()
	project.UUIDModel.UpdatedAt = time.Now()

	data := structToMap(project)

	// Check that fields are properly mapped
	if data["name"] != "Map Test Project" {
		t.Errorf("Expected name to be 'Map Test Project', got %v", data["name"])
	}
	if data["description"] != "Testing structToMap for UUIDModel" {
		t.Errorf("Expected description, got %v", data["description"])
	}

	// Check that UUID ID is included (for UUIDModel)
	if data["id"] != "test-uuid-123" {
		t.Errorf("Expected id to be 'test-uuid-123', got %v", data["id"])
	}

	// Check that timestamps are included
	if _, ok := data["created_at"]; !ok {
		t.Error("Expected created_at to be in map")
	}
	if _, ok := data["updated_at"]; !ok {
		t.Error("Expected updated_at to be in map")
	}
}
