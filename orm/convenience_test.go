package orm

import (
	"errors"
	"testing"
)

// setupConvenienceTests initialises an in-memory SQLite database with the
// test_users table and sets the default manager so static model methods work.
func setupConvenienceTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()
	_, err := db.Exec(`CREATE TABLE test_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT UNIQUE NOT NULL,
		age INTEGER,
		is_active BOOLEAN,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("Failed to create test table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Close()
	})
	return manager
}

// seedUser inserts a user directly via raw SQL and returns the inserted id.
func seedUser(t *testing.T, m *Manager, name, email string, age int) int64 {
	t.Helper()
	result, err := m.DB().Exec(
		"INSERT INTO test_users (name, email, age, is_active, created_at, updated_at) VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))",
		name, email, age, true,
	)
	if err != nil {
		t.Fatalf("Failed to seed user: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

// ---------------------------------------------------------------------------
// 1. FindOrFail / FirstOrFail
// ---------------------------------------------------------------------------

func TestFindOrFail_Found(t *testing.T) {
	setupConvenienceTests(t)
	id := seedUser(t, Default(), "Alice", "alice@example.com", 30)

	user, err := Model[TestUser]{}.FindOrFail(id)
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", user.Name)
	}
}

func TestFindOrFail_NotFound(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.FindOrFail(999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFirstOrFail_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Bob", "bob@example.com", 25)

	user, err := Model[TestUser]{}.FirstOrFail()
	if err != nil {
		t.Fatalf("FirstOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Bob" {
		t.Errorf("expected name 'Bob', got %q", user.Name)
	}
}

func TestFirstOrFail_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.FirstOrFail()
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on empty table, got %v", err)
	}
}

func TestQueryFirstOrFail_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Carol", "carol@example.com", 28)

	var user TestUser
	err := Model[TestUser]{}.Where("name = ?", "Carol").FirstOrFail(&user)
	if err != nil {
		t.Fatalf("Query.FirstOrFail returned unexpected error: %v", err)
	}
	if user.Email != "carol@example.com" {
		t.Errorf("expected email carol@example.com, got %q", user.Email)
	}
}

func TestQueryFirstOrFail_NotFound(t *testing.T) {
	setupConvenienceTests(t)

	var user TestUser
	err := Model[TestUser]{}.Where("name = ?", "nobody").FirstOrFail(&user)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. FirstOrCreate / UpdateOrCreate
// ---------------------------------------------------------------------------

func TestFirstOrCreate_Creates(t *testing.T) {
	setupConvenienceTests(t)

	user, err := Model[TestUser]{}.FirstOrCreate(
		map[string]any{"email": "new@example.com"},
		map[string]any{"name": "New User", "age": 20},
	)
	if err != nil {
		t.Fatalf("FirstOrCreate returned error: %v", err)
	}
	if user.Email != "new@example.com" {
		t.Errorf("expected email 'new@example.com', got %q", user.Email)
	}
	if user.Name != "New User" {
		t.Errorf("expected name 'New User', got %q", user.Name)
	}
	if user.Age != 20 {
		t.Errorf("expected age 20, got %d", user.Age)
	}
}

func TestFirstOrCreate_FindsExisting(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Existing", "existing@example.com", 40)

	user, err := Model[TestUser]{}.FirstOrCreate(
		map[string]any{"email": "existing@example.com"},
		map[string]any{"name": "Should Not Be Used", "age": 99},
	)
	if err != nil {
		t.Fatalf("FirstOrCreate returned error: %v", err)
	}
	// Should return the existing user, NOT the values
	if user.Name != "Existing" {
		t.Errorf("expected name 'Existing', got %q", user.Name)
	}
	if user.Age != 40 {
		t.Errorf("expected age 40 (original), got %d", user.Age)
	}
}

func TestFirstOrCreate_InvalidIdentifier(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.FirstOrCreate(
		map[string]any{"email; DROP TABLE": "evil"},
		map[string]any{},
	)
	if err == nil {
		t.Error("expected error for invalid identifier, got nil")
	}
}

func TestUpdateOrCreate_Creates(t *testing.T) {
	setupConvenienceTests(t)

	user, err := Model[TestUser]{}.UpdateOrCreate(
		map[string]any{"email": "brand-new@example.com"},
		map[string]any{"name": "Brand New", "age": 18},
	)
	if err != nil {
		t.Fatalf("UpdateOrCreate returned error: %v", err)
	}
	if user.Name != "Brand New" {
		t.Errorf("expected name 'Brand New', got %q", user.Name)
	}
}

func TestUpdateOrCreate_Updates(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Old Name", "update@example.com", 30)

	user, err := Model[TestUser]{}.UpdateOrCreate(
		map[string]any{"email": "update@example.com"},
		map[string]any{"name": "Updated Name", "age": 31},
	)
	if err != nil {
		t.Fatalf("UpdateOrCreate returned error: %v", err)
	}
	if user.Name != "Updated Name" {
		t.Errorf("expected name 'Updated Name', got %q", user.Name)
	}
	if user.Age != 31 {
		t.Errorf("expected age 31, got %d", user.Age)
	}

	// Verify only one record exists
	count, err := Model[TestUser]{}.Count()
	if err != nil {
		t.Fatalf("Count returned error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 record after UpdateOrCreate, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// 3. Increment / Decrement
// ---------------------------------------------------------------------------

func TestIncrement_Default(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Inc", "inc@example.com", 10)

	err := Model[TestUser]{}.Where("email = ?", "inc@example.com").Increment("age")
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "inc@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 11 {
		t.Errorf("expected age 11 after Increment, got %d", user.Age)
	}
}

func TestIncrement_CustomAmount(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Inc5", "inc5@example.com", 10)

	err := Model[TestUser]{}.Where("email = ?", "inc5@example.com").Increment("age", 5)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "inc5@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 15 {
		t.Errorf("expected age 15 after Increment(5), got %d", user.Age)
	}
}

func TestDecrement_Default(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Dec", "dec@example.com", 10)

	err := Model[TestUser]{}.Where("email = ?", "dec@example.com").Decrement("age")
	if err != nil {
		t.Fatalf("Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "dec@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 9 {
		t.Errorf("expected age 9 after Decrement, got %d", user.Age)
	}
}

func TestDecrement_CustomAmount(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Dec3", "dec3@example.com", 10)

	err := Model[TestUser]{}.Where("email = ?", "dec3@example.com").Decrement("age", 3)
	if err != nil {
		t.Fatalf("Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "dec3@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 7 {
		t.Errorf("expected age 7 after Decrement(3), got %d", user.Age)
	}
}

func TestIncrement_WithConditions(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 10)
	seedUser(t, Default(), "B", "b@example.com", 20)

	// Only increment A
	err := Model[TestUser]{}.Where("name = ?", "A").Increment("age", 100)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	userA, _ := Model[TestUser]{}.FindBy("email", "a@example.com")
	userB, _ := Model[TestUser]{}.FindBy("email", "b@example.com")

	if userA.Age != 110 {
		t.Errorf("expected A age 110, got %d", userA.Age)
	}
	if userB.Age != 20 {
		t.Errorf("expected B age unchanged at 20, got %d", userB.Age)
	}
}

func TestIncrement_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	err := Model[TestUser]{}.Where("id = ?", 1).Increment("bad column!")
	if err == nil {
		t.Error("expected error for invalid column, got nil")
	}
}

// ---------------------------------------------------------------------------
// 4. Aggregates: Sum, Avg, Min, Max
// ---------------------------------------------------------------------------

func TestSum_WithData(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 10)
	seedUser(t, Default(), "B", "b@example.com", 20)
	seedUser(t, Default(), "C", "c@example.com", 30)

	sum, err := newQuery[TestUser]().Sum("age")
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if sum != 60 {
		t.Errorf("expected Sum(age) = 60, got %f", sum)
	}
}

func TestSum_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	sum, err := newQuery[TestUser]().Sum("age")
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if sum != 0 {
		t.Errorf("expected Sum(age) = 0 for empty table, got %f", sum)
	}
}

func TestSum_WithConditions(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 10)
	seedUser(t, Default(), "B", "b@example.com", 20)

	sum, err := newQuery[TestUser]().Where("name = ?", "A").Sum("age")
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if sum != 10 {
		t.Errorf("expected Sum(age) = 10 for filtered query, got %f", sum)
	}
}

func TestAvg_WithData(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 10)
	seedUser(t, Default(), "B", "b@example.com", 20)

	avg, err := newQuery[TestUser]().Avg("age")
	if err != nil {
		t.Fatalf("Avg returned error: %v", err)
	}
	if avg != 15 {
		t.Errorf("expected Avg(age) = 15, got %f", avg)
	}
}

func TestAvg_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	avg, err := newQuery[TestUser]().Avg("age")
	if err != nil {
		t.Fatalf("Avg returned error: %v", err)
	}
	if avg != 0 {
		t.Errorf("expected Avg(age) = 0 for empty table, got %f", avg)
	}
}

func TestMin_WithData(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 5)
	seedUser(t, Default(), "B", "b@example.com", 15)
	seedUser(t, Default(), "C", "c@example.com", 25)

	min, err := newQuery[TestUser]().Min("age")
	if err != nil {
		t.Fatalf("Min returned error: %v", err)
	}
	if min != 5 {
		t.Errorf("expected Min(age) = 5, got %f", min)
	}
}

func TestMin_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	min, err := newQuery[TestUser]().Min("age")
	if err != nil {
		t.Fatalf("Min returned error: %v", err)
	}
	if min != 0 {
		t.Errorf("expected Min(age) = 0 for empty table, got %f", min)
	}
}

func TestMax_WithData(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "A", "a@example.com", 5)
	seedUser(t, Default(), "B", "b@example.com", 15)
	seedUser(t, Default(), "C", "c@example.com", 25)

	max, err := newQuery[TestUser]().Max("age")
	if err != nil {
		t.Fatalf("Max returned error: %v", err)
	}
	if max != 25 {
		t.Errorf("expected Max(age) = 25, got %f", max)
	}
}

func TestMax_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	max, err := newQuery[TestUser]().Max("age")
	if err != nil {
		t.Fatalf("Max returned error: %v", err)
	}
	if max != 0 {
		t.Errorf("expected Max(age) = 0 for empty table, got %f", max)
	}
}

func TestAggregate_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	_, err := newQuery[TestUser]().Sum("invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Sum, got nil")
	}

	_, err = newQuery[TestUser]().Avg("invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Avg, got nil")
	}

	_, err = newQuery[TestUser]().Min("invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Min, got nil")
	}

	_, err = newQuery[TestUser]().Max("invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Max, got nil")
	}
}

// ---------------------------------------------------------------------------
// 5. When
// ---------------------------------------------------------------------------

func TestWhen_TrueApplies(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Alpha", "alpha@example.com", 30)
	seedUser(t, Default(), "Beta", "beta@example.com", 20)

	users, err := newQuery[TestUser]().
		When(true, func(q *Query[TestUser]) *Query[TestUser] {
			return q.OrderBy("age", "ASC")
		}).Get()
	if err != nil {
		t.Fatalf("When(true) returned error: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].Name != "Beta" {
		t.Errorf("expected first user to be Beta (age 20), got %q", users[0].Name)
	}
}

func TestWhen_FalseSkips(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Alpha", "alpha@example.com", 30)
	seedUser(t, Default(), "Beta", "beta@example.com", 20)

	// When false, the ordering callback should NOT be applied,
	// so results come in insertion order (Alpha first).
	users, err := newQuery[TestUser]().
		When(false, func(q *Query[TestUser]) *Query[TestUser] {
			return q.OrderBy("age", "ASC")
		}).Get()
	if err != nil {
		t.Fatalf("When(false) returned error: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	// Without ordering, insertion order gives Alpha first
	if users[0].Name != "Alpha" {
		t.Errorf("expected first user to be Alpha (insertion order), got %q", users[0].Name)
	}
}

func TestWhen_Chaining(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "X", "x@example.com", 100)
	seedUser(t, Default(), "Y", "y@example.com", 200)

	// Chain multiple When calls
	sum, err := newQuery[TestUser]().
		When(true, func(q *Query[TestUser]) *Query[TestUser] {
			return q.Where("age >= ?", 100)
		}).
		When(false, func(q *Query[TestUser]) *Query[TestUser] {
			return q.Where("age >= ?", 200) // should NOT apply
		}).
		Sum("age")
	if err != nil {
		t.Fatalf("Chained When returned error: %v", err)
	}
	if sum != 300 {
		t.Errorf("expected sum 300, got %f", sum)
	}
}

// ---------------------------------------------------------------------------
// 6. DoesntExist
// ---------------------------------------------------------------------------

func TestDoesntExist_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	if !newQuery[TestUser]().DoesntExist() {
		t.Error("expected DoesntExist() = true for empty table")
	}
	m := Model[TestUser]{}
	if !m.DoesntExist() {
		t.Error("expected Model.DoesntExist() = true for empty table")
	}
}

func TestDoesntExist_NonEmptyTable(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Exists", "exists@example.com", 25)

	if newQuery[TestUser]().DoesntExist() {
		t.Error("expected DoesntExist() = false for non-empty table")
	}
	m2 := Model[TestUser]{}
	if m2.DoesntExist() {
		t.Error("expected Model.DoesntExist() = false for non-empty table")
	}
}

func TestDoesntExist_WithConditions(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Present", "present@example.com", 25)

	// Condition that matches no rows
	if !newQuery[TestUser]().Where("name = ?", "ghost").DoesntExist() {
		t.Error("expected DoesntExist() = true for non-matching condition")
	}

	// Condition that matches
	if newQuery[TestUser]().Where("name = ?", "Present").DoesntExist() {
		t.Error("expected DoesntExist() = false for matching condition")
	}
}

// ---------------------------------------------------------------------------
// 7. Value
// ---------------------------------------------------------------------------

func TestValue_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "ValUser", "val@example.com", 42)

	val, err := newQuery[TestUser]().Where("email = ?", "val@example.com").Value("name")
	if err != nil {
		t.Fatalf("Value returned error: %v", err)
	}
	// SQLite returns string as []byte or string; check the underlying value
	name, ok := val.(string)
	if !ok {
		t.Fatalf("expected string type, got %T", val)
	}
	if name != "ValUser" {
		t.Errorf("expected 'ValUser', got %q", name)
	}
}

func TestValue_NotFound(t *testing.T) {
	setupConvenienceTests(t)

	_, err := newQuery[TestUser]().Where("email = ?", "nobody@example.com").Value("name")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestValue_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	_, err := newQuery[TestUser]().Value("bad column!")
	if err == nil {
		t.Error("expected error for invalid column, got nil")
	}
}

func TestValue_NumericColumn(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "NumUser", "num@example.com", 77)

	val, err := newQuery[TestUser]().Where("email = ?", "num@example.com").Value("age")
	if err != nil {
		t.Fatalf("Value returned error: %v", err)
	}
	// SQLite returns integers as int64
	age, ok := val.(int64)
	if !ok {
		t.Fatalf("expected int64 type, got %T", val)
	}
	if age != 77 {
		t.Errorf("expected age 77, got %d", age)
	}
}

// ---------------------------------------------------------------------------
// Model static Increment/Decrement (delegates to Query)
// ---------------------------------------------------------------------------

func TestModelStaticIncrement(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "StaticInc", "static-inc@example.com", 50)

	// Increment all records
	err := Model[TestUser]{}.Increment("age", 10)
	if err != nil {
		t.Fatalf("Model.Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "static-inc@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 60 {
		t.Errorf("expected age 60, got %d", user.Age)
	}
}

func TestModelStaticDecrement(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "StaticDec", "static-dec@example.com", 50)

	err := Model[TestUser]{}.Decrement("age", 10)
	if err != nil {
		t.Fatalf("Model.Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy("email", "static-dec@example.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 40 {
		t.Errorf("expected age 40, got %d", user.Age)
	}
}

// ---------------------------------------------------------------------------
// UUIDModel variant tests
// ---------------------------------------------------------------------------

func setupUUIDConvenienceTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()
	_, err := db.Exec(`CREATE TABLE test_projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("Failed to create test_projects table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Close()
	})
	return manager
}

func seedProject(t *testing.T, m *Manager, id, name, description string) {
	t.Helper()
	_, err := m.DB().Exec(
		"INSERT INTO test_projects (id, name, description, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))",
		id, name, description,
	)
	if err != nil {
		t.Fatalf("Failed to seed project: %v", err)
	}
}

func TestUUIDModel_FindOrFail_Found(t *testing.T) {
	setupUUIDConvenienceTests(t)
	seedProject(t, Default(), "uuid-1", "Project A", "Desc A")

	proj, err := UUIDModel[TestProject]{}.FindOrFail("uuid-1")
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if proj.Name != "Project A" {
		t.Errorf("expected name 'Project A', got %q", proj.Name)
	}
}

func TestUUIDModel_FindOrFail_NotFound(t *testing.T) {
	setupUUIDConvenienceTests(t)

	_, err := UUIDModel[TestProject]{}.FindOrFail("nonexistent-uuid")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	// Also verify it wraps sql.ErrNoRows
	if !errors.Is(err, ErrRecordNotFound) {
		t.Errorf("expected ErrNotFound to wrap sql.ErrNoRows, but errors.Is returned false")
	}
}

func TestUUIDModel_FirstOrFail_Found(t *testing.T) {
	setupUUIDConvenienceTests(t)
	seedProject(t, Default(), "uuid-2", "First Project", "Description")

	proj, err := UUIDModel[TestProject]{}.FirstOrFail()
	if err != nil {
		t.Fatalf("FirstOrFail returned unexpected error: %v", err)
	}
	if proj.Name != "First Project" {
		t.Errorf("expected name 'First Project', got %q", proj.Name)
	}
}

func TestUUIDModel_FirstOrCreate(t *testing.T) {
	setupUUIDConvenienceTests(t)

	proj, err := UUIDModel[TestProject]{}.FirstOrCreate(
		map[string]any{"name": "New Project"},
		map[string]any{"description": "Auto-created"},
	)
	if err != nil {
		t.Fatalf("FirstOrCreate returned error: %v", err)
	}
	if proj.Name != "New Project" {
		t.Errorf("expected name 'New Project', got %q", proj.Name)
	}
	if proj.UUIDModel.ID == "" {
		t.Error("expected UUID ID to be generated")
	}
}

func TestUUIDModel_UpdateOrCreate(t *testing.T) {
	setupUUIDConvenienceTests(t)
	seedProject(t, Default(), "uuid-up", "Old Name", "Old Desc")

	proj, err := UUIDModel[TestProject]{}.UpdateOrCreate(
		map[string]any{"name": "Old Name"},
		map[string]any{"description": "Updated Desc"},
	)
	if err != nil {
		t.Fatalf("UpdateOrCreate returned error: %v", err)
	}
	if proj.Description != "Updated Desc" {
		t.Errorf("expected description 'Updated Desc', got %q", proj.Description)
	}

	count, _ := UUIDModel[TestProject]{}.Count()
	if count != 1 {
		t.Errorf("expected 1 record after UpdateOrCreate, got %d", count)
	}
}

func TestUUIDModel_DoesntExist(t *testing.T) {
	setupUUIDConvenienceTests(t)

	if !(UUIDModel[TestProject]{}).DoesntExist() {
		t.Error("expected DoesntExist() = true for empty table")
	}

	seedProject(t, Default(), "uuid-ex", "Exists", "yes")

	if (UUIDModel[TestProject]{}).DoesntExist() {
		t.Error("expected DoesntExist() = false after seeding")
	}
}

// ---------------------------------------------------------------------------
// SoftDeleteModel variant tests
// ---------------------------------------------------------------------------

// SoftDeleteUser is a test model with soft delete support for convenience tests.
type SoftDeleteUser struct {
	SoftDeleteModel[SoftDeleteUser]
	Name  string `orm:"column:name"`
	Email string `orm:"column:email;unique"`
	Age   int    `orm:"column:age"`
}

func (SoftDeleteUser) TableName() string {
	return "soft_delete_users"
}

func setupSoftDeleteConvenienceTests(t *testing.T) *Manager {
	t.Helper()
	manager := newTestManager(t)
	db := manager.DB()
	_, err := db.Exec(`CREATE TABLE soft_delete_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT UNIQUE NOT NULL,
		age INTEGER,
		created_at DATETIME,
		updated_at DATETIME,
		deleted_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("Failed to create soft_delete_users table: %v", err)
	}
	SetDefault(manager)
	t.Cleanup(func() {
		ResetDefault()
		manager.Close()
	})
	return manager
}

func seedSoftDeleteUser(t *testing.T, m *Manager, name, email string, age int) int64 {
	t.Helper()
	result, err := m.DB().Exec(
		"INSERT INTO soft_delete_users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))",
		name, email, age,
	)
	if err != nil {
		t.Fatalf("Failed to seed soft delete user: %v", err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestSoftDeleteModel_FindOrFail_Found(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)
	id := seedSoftDeleteUser(t, Default(), "Alice", "alice@sd.com", 30)

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FindOrFail(id)
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", user.Name)
	}
}

func TestSoftDeleteModel_FindOrFail_NotFound(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	_, err := SoftDeleteModel[SoftDeleteUser]{}.FindOrFail(999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSoftDeleteModel_FirstOrCreate(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FirstOrCreate(
		map[string]any{"email": "new@sd.com"},
		map[string]any{"name": "New SD User", "age": 25},
	)
	if err != nil {
		t.Fatalf("FirstOrCreate returned error: %v", err)
	}
	if user.Name != "New SD User" {
		t.Errorf("expected name 'New SD User', got %q", user.Name)
	}
}

func TestSoftDeleteModel_Increment(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)
	seedSoftDeleteUser(t, Default(), "Inc", "inc@sd.com", 10)

	err := SoftDeleteModel[SoftDeleteUser]{}.Increment("age", 5)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FindBy("email", "inc@sd.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 15 {
		t.Errorf("expected age 15, got %d", user.Age)
	}
}

func TestSoftDeleteModel_DoesntExist(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	if !(SoftDeleteModel[SoftDeleteUser]{}).DoesntExist() {
		t.Error("expected DoesntExist() = true for empty table")
	}
}

// ---------------------------------------------------------------------------
// Additional error path tests
// ---------------------------------------------------------------------------

func TestUpdateOrCreate_InvalidIdentifier(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.UpdateOrCreate(
		map[string]any{"email; DROP TABLE": "evil"},
		map[string]any{},
	)
	if err == nil {
		t.Error("expected error for invalid identifier in conditions, got nil")
	}

	_, err = Model[TestUser]{}.UpdateOrCreate(
		map[string]any{"email": "ok@test.com"},
		map[string]any{"bad column!": "value"},
	)
	if err == nil {
		t.Error("expected error for invalid identifier in values, got nil")
	}
}

func TestDecrement_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	err := Model[TestUser]{}.Where("id = ?", 1).Decrement("bad column!")
	if err == nil {
		t.Error("expected error for invalid column in Decrement, got nil")
	}
}

func TestFirstOrCreate_ConstraintViolation(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Existing", "dup@example.com", 30)

	// Try to create with a unique email that already exists but different conditions
	// The conditions don't match (name != "Different"), so it tries to create,
	// which should fail on the unique email constraint.
	_, err := Model[TestUser]{}.FirstOrCreate(
		map[string]any{"name": "Different"},
		map[string]any{"email": "dup@example.com", "age": 25},
	)
	if err == nil {
		t.Error("expected error on unique constraint violation, got nil")
	}
}

func TestErrNotFound_WrapsErrNoRows(t *testing.T) {
	// Verify the wrapping relationship
	if !errors.Is(ErrNotFound, ErrRecordNotFound) {
		t.Error("expected ErrNotFound to wrap sql.ErrNoRows (ErrRecordNotFound)")
	}
}
