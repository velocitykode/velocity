package orm

import (
	"context"
	"reflect"
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

// TestModelSave_RespectsCallerCreatedAt verifies that Save() does not
// clobber a caller-set CreatedAt on insert, but stamps it (and UpdatedAt
// to match) when CreatedAt is zero.
func TestModelSave_RespectsCallerCreatedAt(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	if _, err := manager.DB().Exec(`
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
	`); err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	db := manager.DB()

	// Case 1: caller-set CreatedAt is preserved end-to-end (in struct AND in DB).
	preset := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	u1 := &TestUser{Name: "Backfill", Email: "backfill@example.com"}
	u1.Model.CreatedAt = preset
	if err := Save(manager, u1); err != nil {
		t.Fatalf("Save with preset CreatedAt: %v", err)
	}
	if !u1.Model.CreatedAt.Equal(preset) {
		t.Errorf("in-memory CreatedAt was clobbered: got %v, want %v", u1.Model.CreatedAt, preset)
	}
	if !u1.Model.UpdatedAt.Equal(preset) {
		t.Errorf("in-memory UpdatedAt should mirror caller CreatedAt on insert: got %v, want %v", u1.Model.UpdatedAt, preset)
	}
	// Re-read from the database via raw SQL: this is what catches a serialization
	// or driver-level bug that silently drops the preset and writes time.Now().
	var dbCreated, dbUpdated time.Time
	if err := db.QueryRow("SELECT created_at, updated_at FROM test_users WHERE id = ?", u1.Model.ID).Scan(&dbCreated, &dbUpdated); err != nil {
		t.Fatalf("read back u1 row: %v", err)
	}
	if !dbCreated.Equal(preset) {
		t.Errorf("persisted created_at != preset: got %v, want %v", dbCreated, preset)
	}
	if !dbUpdated.Equal(preset) {
		t.Errorf("persisted updated_at should mirror preset created_at on insert: got %v, want %v", dbUpdated, preset)
	}

	// Case 2: zero CreatedAt is auto-stamped near now (in struct AND in DB).
	u2 := &TestUser{Name: "Fresh", Email: "fresh@example.com"}
	before := time.Now()
	if err := Save(manager, u2); err != nil {
		t.Fatalf("Save with zero CreatedAt: %v", err)
	}
	after := time.Now()
	if u2.Model.CreatedAt.IsZero() {
		t.Fatal("Expected in-memory CreatedAt to be auto-stamped")
	}
	if u2.Model.CreatedAt.Before(before.Add(-time.Second)) || u2.Model.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("in-memory CreatedAt %v not within 1s of now [%v, %v]", u2.Model.CreatedAt, before, after)
	}
	if !u2.Model.UpdatedAt.Equal(u2.Model.CreatedAt) {
		t.Errorf("in-memory UpdatedAt should equal CreatedAt on insert: got %v vs %v", u2.Model.UpdatedAt, u2.Model.CreatedAt)
	}
	if err := db.QueryRow("SELECT created_at, updated_at FROM test_users WHERE id = ?", u2.Model.ID).Scan(&dbCreated, &dbUpdated); err != nil {
		t.Fatalf("read back u2 row: %v", err)
	}
	if dbCreated.Before(before.Add(-2*time.Second)) || dbCreated.After(after.Add(2*time.Second)) {
		t.Errorf("persisted created_at %v not within 2s of now [%v, %v]", dbCreated, before, after)
	}
	if !dbUpdated.Equal(dbCreated) {
		t.Errorf("persisted updated_at should equal created_at on insert: got %v vs %v", dbUpdated, dbCreated)
	}
}

// TestUUIDModelSave_RespectsCallerCreatedAt mirrors the auto-increment
// case for the UUID-keyed save path.
func TestUUIDModelSave_RespectsCallerCreatedAt(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	if _, err := manager.DB().Exec(`
		CREATE TABLE test_projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`); err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}

	db := manager.DB()

	preset := time.Date(2020, 6, 15, 12, 0, 0, 0, time.UTC)
	p1 := &TestProject{Name: "Imported", Description: "from legacy"}
	p1.UUIDModel.CreatedAt = preset
	if err := Save(manager, p1); err != nil {
		t.Fatalf("Save with preset CreatedAt: %v", err)
	}
	if !p1.UUIDModel.CreatedAt.Equal(preset) {
		t.Errorf("in-memory CreatedAt was clobbered: got %v, want %v", p1.UUIDModel.CreatedAt, preset)
	}
	if !p1.UUIDModel.UpdatedAt.Equal(preset) {
		t.Errorf("in-memory UpdatedAt should mirror caller CreatedAt on insert: got %v, want %v", p1.UUIDModel.UpdatedAt, preset)
	}
	// Re-read from DB to defeat any serialization-layer bug.
	var dbCreated, dbUpdated time.Time
	if err := db.QueryRow("SELECT created_at, updated_at FROM test_projects WHERE id = ?", p1.UUIDModel.ID).Scan(&dbCreated, &dbUpdated); err != nil {
		t.Fatalf("read back p1 row: %v", err)
	}
	if !dbCreated.Equal(preset) {
		t.Errorf("persisted created_at != preset: got %v, want %v", dbCreated, preset)
	}
	if !dbUpdated.Equal(preset) {
		t.Errorf("persisted updated_at should mirror preset created_at on insert: got %v, want %v", dbUpdated, preset)
	}

	p2 := &TestProject{Name: "Fresh", Description: "new"}
	before := time.Now()
	if err := Save(manager, p2); err != nil {
		t.Fatalf("Save with zero CreatedAt: %v", err)
	}
	after := time.Now()
	if p2.UUIDModel.CreatedAt.IsZero() {
		t.Fatal("Expected in-memory CreatedAt to be auto-stamped")
	}
	if p2.UUIDModel.CreatedAt.Before(before.Add(-time.Second)) || p2.UUIDModel.CreatedAt.After(after.Add(time.Second)) {
		t.Errorf("in-memory CreatedAt %v not within 1s of now [%v, %v]", p2.UUIDModel.CreatedAt, before, after)
	}
	if !p2.UUIDModel.UpdatedAt.Equal(p2.UUIDModel.CreatedAt) {
		t.Errorf("in-memory UpdatedAt should equal CreatedAt on insert: got %v vs %v", p2.UUIDModel.UpdatedAt, p2.UUIDModel.CreatedAt)
	}
	if err := db.QueryRow("SELECT created_at, updated_at FROM test_projects WHERE id = ?", p2.UUIDModel.ID).Scan(&dbCreated, &dbUpdated); err != nil {
		t.Fatalf("read back p2 row: %v", err)
	}
	if dbCreated.Before(before.Add(-2*time.Second)) || dbCreated.After(after.Add(2*time.Second)) {
		t.Errorf("persisted created_at %v not within 2s of now [%v, %v]", dbCreated, before, after)
	}
	if !dbUpdated.Equal(dbCreated) {
		t.Errorf("persisted updated_at should equal created_at on insert: got %v vs %v", dbUpdated, dbCreated)
	}
}

// legacyColumnModel exercises the asymmetric-column-tag bug: Go field name
// "RenamedField" but DB column is "legacy_xyz". structToMap honors the tag
// (writes to legacy_xyz); mapToStruct must honor it on the read path too.
type legacyColumnModel struct {
	Model[legacyColumnModel]
	RenamedField string `orm:"column:legacy_xyz"`
	Plain        string
}

func (legacyColumnModel) TableName() string { return "legacy_column_models" }

func TestMapToStruct_HonorsColumnTag(t *testing.T) {
	var dst legacyColumnModel
	src := map[string]any{
		"legacy_xyz": "abc",
		"plain":      "ok",
	}
	if err := mapToStruct(src, &dst); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	if dst.RenamedField != "abc" {
		t.Errorf("RenamedField: expected %q from column:legacy_xyz, got %q", "abc", dst.RenamedField)
	}
	if dst.Plain != "ok" {
		t.Errorf("Plain: expected %q, got %q", "ok", dst.Plain)
	}
}

func TestMapToStruct_IgnoresFieldNameWhenColumnTagPresent(t *testing.T) {
	// With an explicit column tag, only the explicit column key should
	// populate the field. The snake_case'd Go field name must not.
	var dst legacyColumnModel
	src := map[string]any{
		"renamed_field": "wrong",
	}
	if err := mapToStruct(src, &dst); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	if dst.RenamedField != "" {
		t.Errorf("RenamedField should be empty when only renamed_field key is supplied; got %q", dst.RenamedField)
	}
}

func TestStructToMap_MapToStruct_RoundTrip(t *testing.T) {
	original := legacyColumnModel{
		RenamedField: "round-trip",
		Plain:        "value",
	}
	encoded := structToMap(&original)

	// structToMap must use the column tag.
	if v, ok := encoded["legacy_xyz"]; !ok || v != "round-trip" {
		t.Fatalf("structToMap should write legacy_xyz=%q, got map=%v", "round-trip", encoded)
	}

	var decoded legacyColumnModel
	if err := mapToStruct(encoded, &decoded); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	if decoded.RenamedField != original.RenamedField {
		t.Errorf("RenamedField round-trip mismatch: got %q want %q", decoded.RenamedField, original.RenamedField)
	}
	if decoded.Plain != original.Plain {
		t.Errorf("Plain round-trip mismatch: got %q want %q", decoded.Plain, original.Plain)
	}
}

// guardedLegacyColumnModel pairs a column-tagged field with a Guarded()
// denylist keyed on the snake_case'd Go FIELD NAME (the user-facing
// contract), not the column name. Used to verify mass-assignment protection
// stays consistent regardless of column-tag renaming.
type guardedLegacyColumnModel struct {
	Model[guardedLegacyColumnModel]
	RenamedField string `orm:"column:legacy_xyz"`
	Plain        string
}

func (guardedLegacyColumnModel) TableName() string { return "guarded_legacy_column_models" }
func (guardedLegacyColumnModel) Guarded() []string {
	// Users protect by Go field name (snake_case), not by column.
	return []string{"renamed_field"}
}

// TestMapToStruct_GuardedHonorsFieldNameNotColumn is the regression test for
// the reviewer-flagged bug: mapToStruct must look up fillable/guarded sets
// by the snake_case'd Go field name (consistent with applyFillableToStruct
// and the user-facing Fillable()/Guarded() contract), even when the field
// is column-tagged. Otherwise an attacker could bypass guards by submitting
// the column key.
func TestMapToStruct_GuardedHonorsFieldNameNotColumn(t *testing.T) {
	var dst guardedLegacyColumnModel
	src := map[string]any{
		"legacy_xyz": "evil", // column key (would slip past column-keyed guard)
		"plain":      "ok",
	}
	if err := mapToStruct(src, &dst); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	if dst.RenamedField != "" {
		t.Errorf("RenamedField must remain empty: Guarded()=[\"renamed_field\"] should block legacy_xyz too; got %q", dst.RenamedField)
	}
	if dst.Plain != "ok" {
		t.Errorf("Plain (not guarded) should be set; got %q", dst.Plain)
	}
}

// fillableLegacyColumnModel is the Fillable counterpart: the allowlist is
// keyed on the field name. mapToStruct must respect that even though the
// map uses the column key.
type fillableLegacyColumnModel struct {
	Model[fillableLegacyColumnModel]
	RenamedField string `orm:"column:legacy_xyz"`
	Plain        string
}

func (fillableLegacyColumnModel) TableName() string { return "fillable_legacy_column_models" }
func (fillableLegacyColumnModel) Fillable() []string {
	// Only "plain" is fillable; "renamed_field" is NOT in the allowlist.
	return []string{"plain"}
}

func TestMapToStruct_FillableHonorsFieldNameNotColumn(t *testing.T) {
	var dst fillableLegacyColumnModel
	src := map[string]any{
		"legacy_xyz": "evil",
		"plain":      "ok",
	}
	if err := mapToStruct(src, &dst); err != nil {
		t.Fatalf("mapToStruct: %v", err)
	}
	if dst.RenamedField != "" {
		t.Errorf("RenamedField must remain empty: Fillable()=[\"plain\"] should reject legacy_xyz; got %q", dst.RenamedField)
	}
	if dst.Plain != "ok" {
		t.Errorf("Plain (fillable) should be set; got %q", dst.Plain)
	}
}

// TestFieldColumnName_DashTagReturnsEmpty verifies the defensive `orm:"-"`
// short-circuit so callers can use empty-string column name as a uniform
// "skip this field" signal (matches resolveColumnName).
func TestFieldColumnName_DashTagReturnsEmpty(t *testing.T) {
	type sample struct {
		Skip string `orm:"-"`
		Keep string
	}
	tt := reflect.TypeOf(sample{})
	if got := fieldColumnName(tt.Field(0)); got != "" {
		t.Errorf("orm:\"-\" should yield empty column name, got %q", got)
	}
	if got := fieldColumnName(tt.Field(1)); got != "keep" {
		t.Errorf("untagged field: expected snake_case fallback %q, got %q", "keep", got)
	}
}

// TestSaveAndFind_HonorsColumnTag is a DB roundtrip smoke test. Save writes
// via structToMap (which honors the column tag); Find reads via
// scanIntoStruct (NOT in scope for this fix). Documented here as a
// follow-up: see TODO below.
//
// NOTE: scanIntoStruct in orm/query.go (~line 1413) re-applies toSnakeCase
// to the resolved column name and has subtly different tag-parsing
// semantics than fieldColumnName. Unifying it onto the helper is a
// follow-up; for now this test only asserts the WRITE side reaches the DB.
// When scanIntoStruct is unified, extend this test to assert the read-back
// value flows through Find as well.
func TestSaveAndFind_HonorsColumnTag(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	if _, err := db.Exec(`CREATE TABLE legacy_column_models (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		legacy_xyz TEXT,
		plain TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(ResetDefault)

	row := &legacyColumnModel{
		RenamedField: "persisted",
		Plain:        "p",
	}
	if err := Save(manager, row); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify the value reached the DB under the column-tagged name.
	var got string
	if err := db.QueryRow("SELECT legacy_xyz FROM legacy_column_models WHERE id = ?", row.Model.ID).Scan(&got); err != nil {
		t.Fatalf("query legacy_xyz: %v", err)
	}
	if got != "persisted" {
		t.Errorf("legacy_xyz column should hold %q, got %q", "persisted", got)
	}
}
