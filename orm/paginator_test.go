package orm

import (
	"fmt"
	"sync"
	"testing"
)

// SoftDeleteTestUser is a model with soft delete support for pagination tests.
type SoftDeleteTestUser struct {
	SoftDeleteModel[SoftDeleteTestUser]
	Name  string `orm:"column:name"`
	Email string `orm:"column:email;unique"`
	Age   int    `orm:"column:age"`
}

func (SoftDeleteTestUser) TableName() string {
	return "soft_delete_test_users"
}

// setupPaginationTable creates a test_users table and inserts count records.
func setupPaginationTable(t *testing.T, manager *Manager, count int) {
	t.Helper()
	db := manager.DB()

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

	for i := 1; i <= count; i++ {
		_, err := db.Exec(
			`INSERT INTO test_users (name, email, age, is_active, created_at, updated_at) VALUES (?, ?, ?, ?, datetime('now'), datetime('now'))`,
			fmt.Sprintf("User %d", i),
			fmt.Sprintf("user%d@example.com", i),
			20+i,
			i%2 == 0,
		)
		if err != nil {
			t.Fatalf("Failed to insert user %d: %v", i, err)
		}
	}
}

// setupSoftDeleteTable creates a soft_delete_test_users table, inserts count
// records, and soft-deletes deleteCount of them (the last N).
func setupSoftDeleteTable(t *testing.T, manager *Manager, count, deleteCount int) {
	t.Helper()
	db := manager.DB()

	_, err := db.Exec(`
		CREATE TABLE soft_delete_test_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			email TEXT UNIQUE NOT NULL,
			age INTEGER,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create soft delete table: %v", err)
	}

	for i := 1; i <= count; i++ {
		_, err := db.Exec(
			`INSERT INTO soft_delete_test_users (name, email, age, created_at, updated_at) VALUES (?, ?, ?, datetime('now'), datetime('now'))`,
			fmt.Sprintf("User %d", i),
			fmt.Sprintf("sd-user%d@example.com", i),
			20+i,
		)
		if err != nil {
			t.Fatalf("Failed to insert user %d: %v", i, err)
		}
	}

	// Soft-delete the last deleteCount records
	if deleteCount > 0 {
		_, err := db.Exec(
			`UPDATE soft_delete_test_users SET deleted_at = datetime('now') WHERE id > ?`,
			count-deleteCount,
		)
		if err != nil {
			t.Fatalf("Failed to soft delete: %v", err)
		}
	}
}

func TestQueryPaginate(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 25)

	tests := []struct {
		name         string
		page         int
		perPage      int
		wantTotal    int
		wantPage     int
		wantPerPage  int
		wantLastPage int
		wantDataLen  int
	}{
		{
			name:         "FirstPage",
			page:         1,
			perPage:      10,
			wantTotal:    25,
			wantPage:     1,
			wantPerPage:  10,
			wantLastPage: 3,
			wantDataLen:  10,
		},
		{
			name:         "MiddlePage",
			page:         2,
			perPage:      10,
			wantTotal:    25,
			wantPage:     2,
			wantPerPage:  10,
			wantLastPage: 3,
			wantDataLen:  10,
		},
		{
			name:         "LastPartialPage",
			page:         3,
			perPage:      10,
			wantTotal:    25,
			wantPage:     3,
			wantPerPage:  10,
			wantLastPage: 3,
			wantDataLen:  5,
		},
		{
			name:         "BeyondLastPage",
			page:         10,
			perPage:      10,
			wantTotal:    25,
			wantPage:     10,
			wantPerPage:  10,
			wantLastPage: 3,
			wantDataLen:  0,
		},
		{
			name:         "ExactFit",
			page:         1,
			perPage:      25,
			wantTotal:    25,
			wantPage:     1,
			wantPerPage:  25,
			wantLastPage: 1,
			wantDataLen:  25,
		},
		{
			name:         "SingleItemPerPage",
			page:         1,
			perPage:      1,
			wantTotal:    25,
			wantPage:     1,
			wantPerPage:  1,
			wantLastPage: 25,
			wantDataLen:  1,
		},
		{
			name:         "LargePerPage",
			page:         1,
			perPage:      100,
			wantTotal:    25,
			wantPage:     1,
			wantPerPage:  100,
			wantLastPage: 1,
			wantDataLen:  25,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := newQuery[TestUser]().Paginate(tt.page, tt.perPage)
			if err != nil {
				t.Fatalf("Paginate(%d, %d) error: %v", tt.page, tt.perPage, err)
			}

			if result.Total() != tt.wantTotal {
				t.Errorf("Total = %d, want %d", result.Total(), tt.wantTotal)
			}
			if result.CurrentPage() != tt.wantPage {
				t.Errorf("CurrentPage = %d, want %d", result.CurrentPage(), tt.wantPage)
			}
			if result.PerPage() != tt.wantPerPage {
				t.Errorf("PerPage = %d, want %d", result.PerPage(), tt.wantPerPage)
			}
			if result.LastPage() != tt.wantLastPage {
				t.Errorf("LastPage = %d, want %d", result.LastPage(), tt.wantLastPage)
			}
			if len(result.Data()) != tt.wantDataLen {
				t.Errorf("len(Data) = %d, want %d", len(result.Data()), tt.wantDataLen)
			}
		})
	}
}

func TestQueryPaginate_InputDefaults(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 20)

	tests := []struct {
		name        string
		page        int
		perPage     int
		wantPage    int
		wantPerPage int
	}{
		{"NegativePage", -1, 10, 1, 10},
		{"ZeroPage", 0, 10, 1, 10},
		{"NegativePerPage", 1, -5, 1, 15},
		{"ZeroPerPage", 1, 0, 1, 15},
		{"BothNegative", -3, -7, 1, 15},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := newQuery[TestUser]().Paginate(tt.page, tt.perPage)
			if err != nil {
				t.Fatalf("Paginate(%d, %d) error: %v", tt.page, tt.perPage, err)
			}
			if result.CurrentPage() != tt.wantPage {
				t.Errorf("CurrentPage = %d, want %d", result.CurrentPage(), tt.wantPage)
			}
			if result.PerPage() != tt.wantPerPage {
				t.Errorf("PerPage = %d, want %d", result.PerPage(), tt.wantPerPage)
			}
		})
	}
}

func TestQueryPaginate_EmptyTable(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 0)

	result, err := newQuery[TestUser]().Paginate(1, 10)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 0 {
		t.Errorf("Total = %d, want 0", result.Total())
	}
	if result.LastPage() != 0 {
		t.Errorf("LastPage = %d, want 0", result.LastPage())
	}
	if len(result.Data()) != 0 {
		t.Errorf("len(Data) = %d, want 0", len(result.Data()))
	}
	if result.Data() == nil {
		// Data should be nil (not an empty slice) when no records, matching Get() behavior
	}
}

func TestQueryPaginate_WithConditions(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 20)

	// Only active users (even IDs → 10 users)
	result, err := TestUser{}.Where("is_active = ?", true).Paginate(1, 5)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 10 {
		t.Errorf("Total = %d, want 10", result.Total())
	}
	if result.LastPage() != 2 {
		t.Errorf("LastPage = %d, want 2", result.LastPage())
	}
	if len(result.Data()) != 5 {
		t.Errorf("len(Data) = %d, want 5", len(result.Data()))
	}
}

func TestQueryPaginate_WithOrdering(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 10)

	result, err := TestUser{}.OrderBy("age", "DESC").Paginate(1, 3)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	data := result.Data()
	if len(data) != 3 {
		t.Fatalf("len(Data) = %d, want 3", len(data))
	}

	// Descending order: highest ages first (ages are 21..30)
	if data[0].Age != 30 {
		t.Errorf("data[0].Age = %d, want 30", data[0].Age)
	}
	if data[1].Age != 29 {
		t.Errorf("data[1].Age = %d, want 29", data[1].Age)
	}
	if data[2].Age != 28 {
		t.Errorf("data[2].Age = %d, want 28", data[2].Age)
	}
}

func TestQueryPaginate_WithConditionsAndOrdering(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 20)

	// Active users ordered by age descending, page 2 of 3-per-page
	result, err := TestUser{}.Where("is_active = ?", true).
		OrderBy("age", "DESC").
		Paginate(2, 3)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 10 {
		t.Errorf("Total = %d, want 10", result.Total())
	}
	if len(result.Data()) != 3 {
		t.Errorf("len(Data) = %d, want 3", len(result.Data()))
	}

	// Verify items are actually ordered descending
	data := result.Data()
	for i := 1; i < len(data); i++ {
		if data[i].Age > data[i-1].Age {
			t.Errorf("data[%d].Age (%d) > data[%d].Age (%d): not descending",
				i, data[i].Age, i-1, data[i-1].Age)
		}
	}
}

func TestQueryPaginate_SoftDeleteExcludesDeleted(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	// 20 total, 5 soft-deleted → 15 visible
	setupSoftDeleteTable(t, manager, 20, 5)

	result, err := SoftDeleteTestUser{}.Paginate(1, 10)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 15 {
		t.Errorf("Total = %d, want 15 (excluding soft-deleted)", result.Total())
	}
	if result.LastPage() != 2 {
		t.Errorf("LastPage = %d, want 2", result.LastPage())
	}
	if len(result.Data()) != 10 {
		t.Errorf("len(Data) = %d, want 10", len(result.Data()))
	}
}

func TestQueryPaginate_SoftDeleteWithTrashed(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	// 20 total, 5 soft-deleted
	setupSoftDeleteTable(t, manager, 20, 5)

	// WithTrashed should include all 20
	result, err := SoftDeleteTestUser{}.WithTrashed().Paginate(1, 10)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 20 {
		t.Errorf("Total = %d, want 20 (including soft-deleted)", result.Total())
	}
	if result.LastPage() != 2 {
		t.Errorf("LastPage = %d, want 2", result.LastPage())
	}
}

func TestQueryPaginate_SoftDeleteOnlyTrashed(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	// 20 total, 5 soft-deleted
	setupSoftDeleteTable(t, manager, 20, 5)

	// OnlyTrashed should return only the 5 deleted
	result, err := SoftDeleteTestUser{}.OnlyTrashed().Paginate(1, 10)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}

	if result.Total() != 5 {
		t.Errorf("Total = %d, want 5 (only soft-deleted)", result.Total())
	}
	if result.LastPage() != 1 {
		t.Errorf("LastPage = %d, want 1", result.LastPage())
	}
	if len(result.Data()) != 5 {
		t.Errorf("len(Data) = %d, want 5", len(result.Data()))
	}
}

func TestQueryPaginate_ErrorOnBadTable(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	// No table created — query should fail
	_, err := newQuery[TestUser]().Paginate(1, 10)
	if err == nil {
		t.Fatal("expected error when paginating non-existent table, got nil")
	}
}

func TestModelPaginate(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 15)

	result, err := TestUser{}.Paginate(2, 5)
	if err != nil {
		t.Fatalf("Model.Paginate error: %v", err)
	}

	if result.Total() != 15 {
		t.Errorf("Total = %d, want 15", result.Total())
	}
	if result.CurrentPage() != 2 {
		t.Errorf("CurrentPage = %d, want 2", result.CurrentPage())
	}
	if result.PerPage() != 5 {
		t.Errorf("PerPage = %d, want 5", result.PerPage())
	}
	if result.LastPage() != 3 {
		t.Errorf("LastPage = %d, want 3", result.LastPage())
	}
	if len(result.Data()) != 5 {
		t.Errorf("len(Data) = %d, want 5", len(result.Data()))
	}
}

func TestSoftDeleteModelPaginate(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	// 10 total, 3 soft-deleted → 7 visible
	setupSoftDeleteTable(t, manager, 10, 3)

	result, err := SoftDeleteTestUser{}.Paginate(1, 5)
	if err != nil {
		t.Fatalf("SoftDeleteModel.Paginate error: %v", err)
	}

	if result.Total() != 7 {
		t.Errorf("Total = %d, want 7", result.Total())
	}
	if result.LastPage() != 2 {
		t.Errorf("LastPage = %d, want 2", result.LastPage())
	}
	if len(result.Data()) != 5 {
		t.Errorf("len(Data) = %d, want 5", len(result.Data()))
	}
}

func TestPaginatedResult_PaginatorInterface(t *testing.T) {
	// Verify all resource.Paginator methods return correct values.
	result := &PaginatedResult[TestUser]{
		items:       []TestUser{{Name: "Alice"}, {Name: "Bob"}},
		total:       42,
		perPage:     10,
		currentPage: 3,
		lastPage:    5,
	}

	if result.Total() != 42 {
		t.Errorf("Total = %d, want 42", result.Total())
	}
	if result.PerPage() != 10 {
		t.Errorf("PerPage = %d, want 10", result.PerPage())
	}
	if result.CurrentPage() != 3 {
		t.Errorf("CurrentPage = %d, want 3", result.CurrentPage())
	}
	if result.LastPage() != 5 {
		t.Errorf("LastPage = %d, want 5", result.LastPage())
	}

	items, ok := result.Items().([]TestUser)
	if !ok {
		t.Fatal("Items() did not return []TestUser")
	}
	if len(items) != 2 {
		t.Errorf("len(Items) = %d, want 2", len(items))
	}

	data := result.Data()
	if len(data) != 2 {
		t.Errorf("len(Data) = %d, want 2", len(data))
	}
	if data[0].Name != "Alice" {
		t.Errorf("Data[0].Name = %q, want %q", data[0].Name, "Alice")
	}
}

func TestPaginatedResult_LastPageCalculation(t *testing.T) {
	tests := []struct {
		name     string
		total    int
		perPage  int
		wantLast int
	}{
		{"ZeroTotal", 0, 10, 0},
		{"ExactDivisor", 30, 10, 3},
		{"Remainder", 31, 10, 4},
		{"OneItem", 1, 10, 1},
		{"OnePerPage", 5, 1, 5},
		{"EqualTotalAndPerPage", 10, 10, 1},
		{"TotalLessThanPerPage", 3, 10, 1},
	}

	manager := newTestManager(t)
	defer manager.Close()
	SetDefault(manager)
	defer ResetDefault()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := manager.DB()

			// Drop and recreate with exact row count
			db.Exec(`DROP TABLE IF EXISTS test_users`)
			setupPaginationTable(t, manager, tt.total)

			result, err := newQuery[TestUser]().Paginate(1, tt.perPage)
			if err != nil {
				t.Fatalf("Paginate(1, %d) error: %v", tt.perPage, err)
			}

			if result.LastPage() != tt.wantLast {
				t.Errorf("LastPage = %d, want %d (total=%d, perPage=%d)",
					result.LastPage(), tt.wantLast, tt.total, tt.perPage)
			}
		})
	}
}

func TestQueryPaginate_Concurrent(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Close()
	// SQLite in-memory: force single connection so all goroutines share
	// the same database (each connection gets its own in-memory DB).
	manager.DB().SetMaxOpenConns(1)
	SetDefault(manager)
	defer ResetDefault()

	setupPaginationTable(t, manager, 50)

	var wg sync.WaitGroup
	errs := make(chan error, 20)

	for i := 1; i <= 10; i++ {
		wg.Add(1)
		go func(page int) {
			defer wg.Done()
			result, err := TestUser{}.Paginate(page, 5)
			if err != nil {
				errs <- fmt.Errorf("page %d: %w", page, err)
				return
			}
			if result.Total() != 50 {
				errs <- fmt.Errorf("page %d: Total = %d, want 50", page, result.Total())
				return
			}
			if result.LastPage() != 10 {
				errs <- fmt.Errorf("page %d: LastPage = %d, want 10", page, result.LastPage())
				return
			}
			if result.PerPage() != 5 {
				errs <- fmt.Errorf("page %d: PerPage = %d, want 5", page, result.PerPage())
				return
			}
			// Each page should have exactly 5 items
			if len(result.Data()) != 5 {
				errs <- fmt.Errorf("page %d: len(Data) = %d, want 5", page, len(result.Data()))
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}
