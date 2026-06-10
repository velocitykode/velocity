package orm

import (
	"context"
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
		manager.Shutdown(context.Background())
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

	user, err := Model[TestUser]{}.FindOrFail(context.Background(), id)
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", user.Name)
	}
}

func TestFindOrFail_NotFound(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.FindOrFail(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFirstOrFail_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Bob", "bob@example.com", 25)

	user, err := Model[TestUser]{}.FirstOrFail(context.Background())
	if err != nil {
		t.Fatalf("FirstOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Bob" {
		t.Errorf("expected name 'Bob', got %q", user.Name)
	}
}

func TestFirstOrFail_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.FirstOrFail(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on empty table, got %v", err)
	}
}

func TestQueryFirstOrFail_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Carol", "carol@example.com", 28)

	var user TestUser
	err := Model[TestUser]{}.Where("name = ?", "Carol").FirstOrFail(context.Background(), &user)
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
	err := Model[TestUser]{}.Where("name = ?", "nobody").FirstOrFail(context.Background(), &user)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 2. FirstOrCreate / UpdateOrCreate
// ---------------------------------------------------------------------------

func TestFirstOrCreate_Creates(t *testing.T) {
	setupConvenienceTests(t)

	user, err := Model[TestUser]{}.FirstOrCreate(context.Background(),
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

	user, err := Model[TestUser]{}.FirstOrCreate(context.Background(),
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

	_, err := Model[TestUser]{}.FirstOrCreate(context.Background(),
		map[string]any{"email; DROP TABLE": "evil"},
		map[string]any{},
	)
	if err == nil {
		t.Error("expected error for invalid identifier, got nil")
	}
}

func TestUpdateOrCreate_Creates(t *testing.T) {
	setupConvenienceTests(t)

	user, err := Model[TestUser]{}.UpdateOrCreate(context.Background(),
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

	user, err := Model[TestUser]{}.UpdateOrCreate(context.Background(),
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
	count, err := Model[TestUser]{}.Count(context.Background())
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

	err := Model[TestUser]{}.Where("email = ?", "inc@example.com").Increment(context.Background(), "age")
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "inc@example.com")
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

	err := Model[TestUser]{}.Where("email = ?", "inc5@example.com").Increment(context.Background(), "age", 5)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "inc5@example.com")
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

	err := Model[TestUser]{}.Where("email = ?", "dec@example.com").Decrement(context.Background(), "age")
	if err != nil {
		t.Fatalf("Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "dec@example.com")
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

	err := Model[TestUser]{}.Where("email = ?", "dec3@example.com").Decrement(context.Background(), "age", 3)
	if err != nil {
		t.Fatalf("Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "dec3@example.com")
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
	err := Model[TestUser]{}.Where("name = ?", "A").Increment(context.Background(), "age", 100)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	userA, _ := Model[TestUser]{}.FindBy(context.Background(), "email", "a@example.com")
	userB, _ := Model[TestUser]{}.FindBy(context.Background(), "email", "b@example.com")

	if userA.Age != 110 {
		t.Errorf("expected A age 110, got %d", userA.Age)
	}
	if userB.Age != 20 {
		t.Errorf("expected B age unchanged at 20, got %d", userB.Age)
	}
}

func TestIncrement_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	err := Model[TestUser]{}.Where("id = ?", 1).Increment(context.Background(), "bad column!")
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

	sum, err := newQuery[TestUser]().Sum(context.Background(), "age")
	if err != nil {
		t.Fatalf("Sum returned error: %v", err)
	}
	if sum != 60 {
		t.Errorf("expected Sum(age) = 60, got %f", sum)
	}
}

func TestSum_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	sum, err := newQuery[TestUser]().Sum(context.Background(), "age")
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

	sum, err := newQuery[TestUser]().Where("name = ?", "A").Sum(context.Background(), "age")
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

	avg, err := newQuery[TestUser]().Avg(context.Background(), "age")
	if err != nil {
		t.Fatalf("Avg returned error: %v", err)
	}
	if avg != 15 {
		t.Errorf("expected Avg(age) = 15, got %f", avg)
	}
}

func TestAvg_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	avg, err := newQuery[TestUser]().Avg(context.Background(), "age")
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

	min, err := newQuery[TestUser]().Min(context.Background(), "age")
	if err != nil {
		t.Fatalf("Min returned error: %v", err)
	}
	if min != 5 {
		t.Errorf("expected Min(age) = 5, got %f", min)
	}
}

func TestMin_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	min, err := newQuery[TestUser]().Min(context.Background(), "age")
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

	max, err := newQuery[TestUser]().Max(context.Background(), "age")
	if err != nil {
		t.Fatalf("Max returned error: %v", err)
	}
	if max != 25 {
		t.Errorf("expected Max(age) = 25, got %f", max)
	}
}

func TestMax_EmptyTable(t *testing.T) {
	setupConvenienceTests(t)

	max, err := newQuery[TestUser]().Max(context.Background(), "age")
	if err != nil {
		t.Fatalf("Max returned error: %v", err)
	}
	if max != 0 {
		t.Errorf("expected Max(age) = 0 for empty table, got %f", max)
	}
}

func TestAggregate_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	_, err := newQuery[TestUser]().Sum(context.Background(), "invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Sum, got nil")
	}

	_, err = newQuery[TestUser]().Avg(context.Background(), "invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Avg, got nil")
	}

	_, err = newQuery[TestUser]().Min(context.Background(), "invalid column!")
	if err == nil {
		t.Error("expected error for invalid column in Min, got nil")
	}

	_, err = newQuery[TestUser]().Max(context.Background(), "invalid column!")
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
		}).Get(context.Background())
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
		}).Get(context.Background())
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
		Sum(context.Background(), "age")
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

	absent, err := newQuery[TestUser]().DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if !absent {
		t.Error("expected DoesntExist() = true for empty table")
	}
	m := Model[TestUser]{}
	absent, err = m.DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("Model.DoesntExist returned error: %v", err)
	}
	if !absent {
		t.Error("expected Model.DoesntExist() = true for empty table")
	}
}

func TestDoesntExist_NonEmptyTable(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Exists", "exists@example.com", 25)

	absent, err := newQuery[TestUser]().DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if absent {
		t.Error("expected DoesntExist() = false for non-empty table")
	}
	m2 := Model[TestUser]{}
	absent, err = m2.DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("Model.DoesntExist returned error: %v", err)
	}
	if absent {
		t.Error("expected Model.DoesntExist() = false for non-empty table")
	}
}

func TestDoesntExist_WithConditions(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "Present", "present@example.com", 25)

	// Condition that matches no rows
	absent, err := newQuery[TestUser]().Where("name = ?", "ghost").DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if !absent {
		t.Error("expected DoesntExist() = true for non-matching condition")
	}

	// Condition that matches
	absent, err = newQuery[TestUser]().Where("name = ?", "Present").DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if absent {
		t.Error("expected DoesntExist() = false for matching condition")
	}
}

// ---------------------------------------------------------------------------
// 7. Value
// ---------------------------------------------------------------------------

func TestValue_Found(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "ValUser", "val@example.com", 42)

	val, err := newQuery[TestUser]().Where("email = ?", "val@example.com").Value(context.Background(), "name")
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

	_, err := newQuery[TestUser]().Where("email = ?", "nobody@example.com").Value(context.Background(), "name")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestValue_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	_, err := newQuery[TestUser]().Value(context.Background(), "bad column!")
	if err == nil {
		t.Error("expected error for invalid column, got nil")
	}
}

func TestValue_NumericColumn(t *testing.T) {
	setupConvenienceTests(t)
	seedUser(t, Default(), "NumUser", "num@example.com", 77)

	val, err := newQuery[TestUser]().Where("email = ?", "num@example.com").Value(context.Background(), "age")
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
	err := Model[TestUser]{}.Increment(context.Background(), "age", 10)
	if err != nil {
		t.Fatalf("Model.Increment returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "static-inc@example.com")
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

	err := Model[TestUser]{}.Decrement(context.Background(), "age", 10)
	if err != nil {
		t.Fatalf("Model.Decrement returned error: %v", err)
	}

	user, err := Model[TestUser]{}.FindBy(context.Background(), "email", "static-dec@example.com")
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
		manager.Shutdown(context.Background())
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

	proj, err := UUIDModel[TestProject]{}.FindOrFail(context.Background(), "uuid-1")
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if proj.Name != "Project A" {
		t.Errorf("expected name 'Project A', got %q", proj.Name)
	}
}

func TestUUIDModel_FindOrFail_NotFound(t *testing.T) {
	setupUUIDConvenienceTests(t)

	_, err := UUIDModel[TestProject]{}.FindOrFail(context.Background(), "nonexistent-uuid")
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

	proj, err := UUIDModel[TestProject]{}.FirstOrFail(context.Background())
	if err != nil {
		t.Fatalf("FirstOrFail returned unexpected error: %v", err)
	}
	if proj.Name != "First Project" {
		t.Errorf("expected name 'First Project', got %q", proj.Name)
	}
}

func TestUUIDModel_FirstOrCreate(t *testing.T) {
	setupUUIDConvenienceTests(t)

	proj, err := UUIDModel[TestProject]{}.FirstOrCreate(context.Background(),
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

	proj, err := UUIDModel[TestProject]{}.UpdateOrCreate(context.Background(),
		map[string]any{"name": "Old Name"},
		map[string]any{"description": "Updated Desc"},
	)
	if err != nil {
		t.Fatalf("UpdateOrCreate returned error: %v", err)
	}
	if proj.Description != "Updated Desc" {
		t.Errorf("expected description 'Updated Desc', got %q", proj.Description)
	}

	count, _ := UUIDModel[TestProject]{}.Count(context.Background())
	if count != 1 {
		t.Errorf("expected 1 record after UpdateOrCreate, got %d", count)
	}
}

func TestUUIDModel_DoesntExist(t *testing.T) {
	setupUUIDConvenienceTests(t)

	absent, err := (UUIDModel[TestProject]{}).DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if !absent {
		t.Error("expected DoesntExist() = true for empty table")
	}

	seedProject(t, Default(), "uuid-ex", "Exists", "yes")

	absent, err = (UUIDModel[TestProject]{}).DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if absent {
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

func (SoftDeleteUser) Fillable() []string {
	return []string{"name", "email", "age"}
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
		manager.Shutdown(context.Background())
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

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FindOrFail(context.Background(), id)
	if err != nil {
		t.Fatalf("FindOrFail returned unexpected error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", user.Name)
	}
}

func TestSoftDeleteModel_FindOrFail_NotFound(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	_, err := SoftDeleteModel[SoftDeleteUser]{}.FindOrFail(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSoftDeleteModel_FirstOrCreate(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FirstOrCreate(context.Background(),
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

	err := SoftDeleteModel[SoftDeleteUser]{}.Increment(context.Background(), "age", 5)
	if err != nil {
		t.Fatalf("Increment returned error: %v", err)
	}

	user, err := SoftDeleteModel[SoftDeleteUser]{}.FindBy(context.Background(), "email", "inc@sd.com")
	if err != nil {
		t.Fatalf("FindBy returned error: %v", err)
	}
	if user.Age != 15 {
		t.Errorf("expected age 15, got %d", user.Age)
	}
}

func TestSoftDeleteModel_DoesntExist(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)

	absent, err := (SoftDeleteModel[SoftDeleteUser]{}).DoesntExist(context.Background())
	if err != nil {
		t.Fatalf("DoesntExist returned error: %v", err)
	}
	if !absent {
		t.Error("expected DoesntExist() = true for empty table")
	}
}

// trashSoftDeleteUser stamps deleted_at directly via raw SQL so the test
// fixture stays decoupled from the framework's Delete pathway. Returns the
// row id for assertions.
func trashSoftDeleteUser(t *testing.T, m *Manager, id int64) {
	t.Helper()
	if _, err := m.DB().Exec(
		"UPDATE soft_delete_users SET deleted_at = datetime('now') WHERE id = ?",
		id,
	); err != nil {
		t.Fatalf("trash soft delete user: %v", err)
	}
}

// TestSoftDeleteModel_Count_HidesTrashed pins the soft-delete scope for the
// Count terminal. Regression: previously Count bypassed the deleted_at
// predicate that Get applied, so paginated lists reported a total that
// included trashed rows even though the visible page hid them.
func TestSoftDeleteModel_Count_HidesTrashed(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)
	m := Default()
	live := seedSoftDeleteUser(t, m, "Live", "live@sd.com", 30)
	trashed := seedSoftDeleteUser(t, m, "Trashed", "trashed@sd.com", 31)
	trashSoftDeleteUser(t, m, trashed)

	got, err := SoftDeleteModel[SoftDeleteUser]{}.Where("id > ?", 0).Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != 1 {
		t.Errorf("default scope: Count() = %d, want 1 (excludes trashed); live id=%d trashed id=%d", got, live, trashed)
	}

	withTrashed, err := SoftDeleteModel[SoftDeleteUser]{}.Where("id > ?", 0).WithTrashed().Count(context.Background())
	if err != nil {
		t.Fatalf("Count WithTrashed: %v", err)
	}
	if withTrashed != 2 {
		t.Errorf("WithTrashed: Count() = %d, want 2", withTrashed)
	}

	onlyTrashed, err := SoftDeleteModel[SoftDeleteUser]{}.Where("id > ?", 0).OnlyTrashed().Count(context.Background())
	if err != nil {
		t.Fatalf("Count OnlyTrashed: %v", err)
	}
	if onlyTrashed != 1 {
		t.Errorf("OnlyTrashed: Count() = %d, want 1", onlyTrashed)
	}
}

// TestSoftDeleteModel_Pluck_HidesTrashed pins the soft-delete scope for the
// Pluck terminal so callers selecting a single column do not leak trashed
// values.
func TestSoftDeleteModel_Pluck_HidesTrashed(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)
	m := Default()
	seedSoftDeleteUser(t, m, "Live", "live@sd.com", 30)
	trashed := seedSoftDeleteUser(t, m, "Trashed", "trashed@sd.com", 31)
	trashSoftDeleteUser(t, m, trashed)

	emails, err := SoftDeleteModel[SoftDeleteUser]{}.Where("id > ?", 0).Pluck(context.Background(), "email")
	if err != nil {
		t.Fatalf("Pluck: %v", err)
	}
	if len(emails) != 1 {
		t.Fatalf("default scope: Pluck returned %d rows, want 1", len(emails))
	}
	if got, _ := emails[0].(string); got != "live@sd.com" {
		t.Errorf("Pluck returned %v, want live@sd.com", emails[0])
	}
}

// TestSoftDeleteModel_Update_HidesTrashed pins the soft-delete scope for
// the Update terminal so a mass update via the fluent builder does not
// silently revive or mutate trashed rows.
func TestSoftDeleteModel_Update_HidesTrashed(t *testing.T) {
	setupSoftDeleteConvenienceTests(t)
	m := Default()
	live := seedSoftDeleteUser(t, m, "Live", "live@sd.com", 30)
	trashed := seedSoftDeleteUser(t, m, "Trashed", "trashed@sd.com", 31)
	trashSoftDeleteUser(t, m, trashed)

	affected, err := SoftDeleteModel[SoftDeleteUser]{}.Where("id > ?", 0).Update(context.Background(), map[string]any{"age": 99})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if affected != 1 {
		t.Errorf("default scope: Update affected %d rows, want 1", affected)
	}

	var trashedRow SoftDeleteUser
	if err := m.DB().QueryRow(
		"SELECT age FROM soft_delete_users WHERE id = ?", trashed,
	).Scan(&trashedRow.Age); err != nil {
		t.Fatalf("read trashed row: %v", err)
	}
	if trashedRow.Age == 99 {
		t.Errorf("trashed row was mutated by default-scope Update (id=%d)", trashed)
	}

	var liveRow SoftDeleteUser
	if err := m.DB().QueryRow(
		"SELECT age FROM soft_delete_users WHERE id = ?", live,
	).Scan(&liveRow.Age); err != nil {
		t.Fatalf("read live row: %v", err)
	}
	if liveRow.Age != 99 {
		t.Errorf("live row was not mutated (id=%d, age=%d)", live, liveRow.Age)
	}
}

// ---------------------------------------------------------------------------
// Additional error path tests
// ---------------------------------------------------------------------------

func TestUpdateOrCreate_InvalidIdentifier(t *testing.T) {
	setupConvenienceTests(t)

	_, err := Model[TestUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"email; DROP TABLE": "evil"},
		map[string]any{},
	)
	if err == nil {
		t.Error("expected error for invalid identifier in conditions, got nil")
	}

	_, err = Model[TestUser]{}.UpdateOrCreate(context.Background(),
		map[string]any{"email": "ok@test.com"},
		map[string]any{"bad column!": "value"},
	)
	if err == nil {
		t.Error("expected error for invalid identifier in values, got nil")
	}
}

func TestDecrement_InvalidColumn(t *testing.T) {
	setupConvenienceTests(t)

	err := Model[TestUser]{}.Where("id = ?", 1).Decrement(context.Background(), "bad column!")
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
	_, err := Model[TestUser]{}.FirstOrCreate(context.Background(),
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
