package orm

import (
	"context"
	"fmt"
	"testing"
)

// CountTestOrder is a minimal model for grouped/distinct count tests.
type CountTestOrder struct {
	Model[CountTestOrder]
	UserID int    `orm:"column:user_id"`
	Status string `orm:"column:status"`
}

func (CountTestOrder) TableName() string {
	return "count_test_orders"
}

// setupCountTable creates count_test_orders and inserts one row per
// (userID, status) pair given.
func setupCountTable(t *testing.T, manager *Manager, rows []struct {
	UserID int
	Status string
}) {
	t.Helper()
	db := manager.DB()

	_, err := db.Exec(`
		CREATE TABLE count_test_orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create count test table: %v", err)
	}

	for i, row := range rows {
		_, err := db.Exec(
			`INSERT INTO count_test_orders (user_id, status, created_at, updated_at) VALUES (?, ?, datetime('now'), datetime('now'))`,
			row.UserID, row.Status,
		)
		if err != nil {
			t.Fatalf("Failed to insert row %d: %v", i, err)
		}
	}
}

// TestCount_GroupBy_RegressionB27 locks the fix for grouped counts: before
// B27, Count dropped GROUP BY/HAVING from the compiled statement (returning
// the per-group count of whichever group the driver scanned first), and
// Paginate inherited the wrong total.
func TestCount_GroupBy_RegressionB27(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	// 5 rows in 2 groups: user 1 has 3 orders, user 2 has 2.
	setupCountTable(t, manager, []struct {
		UserID int
		Status string
	}{
		{1, "active"},
		{1, "active"},
		{1, "done"},
		{2, "active"},
		{2, "done"},
	})

	count, err := newQuery[CountTestOrder]().GroupBy("user_id").Count(context.Background())
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if count != 2 {
		t.Errorf("Count with GroupBy = %d, want 2 (number of groups)", count)
	}

	// HAVING on a grouped column narrows the group count.
	havingCount, err := newQuery[CountTestOrder]().
		GroupBy("user_id").
		Having("user_id > ?", 1).
		Count(context.Background())
	if err != nil {
		t.Fatalf("Count with Having error: %v", err)
	}
	if havingCount != 1 {
		t.Errorf("Count with GroupBy+Having = %d, want 1", havingCount)
	}

	// Paginate shares the same count path; total must be group count.
	result, err := newQuery[CountTestOrder]().GroupBy("user_id").Paginate(context.Background(), 1, 10)
	if err != nil {
		t.Fatalf("Paginate error: %v", err)
	}
	if result.Total() != 2 {
		t.Errorf("Paginate Total with GroupBy = %d, want 2", result.Total())
	}
}

// TestCount_Distinct verifies Count counts the distinct selected column
// set instead of compiling SELECT DISTINCT COUNT(*) (where DISTINCT is a
// no-op on the single aggregate row).
func TestCount_Distinct(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	// 5 rows, 3 distinct statuses.
	setupCountTable(t, manager, []struct {
		UserID int
		Status string
	}{
		{1, "active"},
		{2, "active"},
		{3, "done"},
		{4, "done"},
		{5, "cancelled"},
	})

	count, err := newQuery[CountTestOrder]().Select("status").Distinct().Count(context.Background())
	if err != nil {
		t.Fatalf("Distinct Count error: %v", err)
	}
	if count != 3 {
		t.Errorf("Distinct Count = %d, want 3 (distinct statuses)", count)
	}

	// Conditions still apply inside the wrapped subquery.
	filtered, err := newQuery[CountTestOrder]().
		Select("status").
		Distinct().
		Where("user_id < ?", 5).
		Count(context.Background())
	if err != nil {
		t.Fatalf("Distinct Count with Where error: %v", err)
	}
	if filtered != 2 {
		t.Errorf("Distinct Count with Where = %d, want 2", filtered)
	}
}

// TestCount_PlainFastPathUnchanged guards the ungrouped, non-distinct fast
// path: a flat COUNT(*) with conditions, no subquery wrap.
func TestCount_PlainFastPathUnchanged(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	setupCountTable(t, manager, []struct {
		UserID int
		Status string
	}{
		{1, "active"},
		{2, "active"},
		{3, "done"},
	})

	count, err := newQuery[CountTestOrder]().Where("status = ?", "active").Count(context.Background())
	if err != nil {
		t.Fatalf("Count error: %v", err)
	}
	if count != 2 {
		t.Errorf("Count = %d, want 2", count)
	}
}

// TestCount_GroupBy_DoesNotMutateSharedSlices guards the Paginate gotcha:
// the paginator's count query shares q.groups/q.having backing arrays with
// the data query, so Count must read them without mutation.
func TestCount_GroupBy_DoesNotMutateSharedSlices(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())
	SetDefault(manager)
	defer ResetDefault()

	setupCountTable(t, manager, []struct {
		UserID int
		Status string
	}{
		{1, "active"},
		{1, "done"},
		{2, "active"},
	})

	q := newQuery[CountTestOrder]().GroupBy("user_id").Having("user_id > ?", 0)
	groupsBefore := fmt.Sprintf("%#v", q.groups)
	havingBefore := fmt.Sprintf("%#v", q.having)

	if _, err := q.Count(context.Background()); err != nil {
		t.Fatalf("Count error: %v", err)
	}

	if got := fmt.Sprintf("%#v", q.groups); got != groupsBefore {
		t.Errorf("Count mutated q.groups: before %s, after %s", groupsBefore, got)
	}
	if got := fmt.Sprintf("%#v", q.having); got != havingBefore {
		t.Errorf("Count mutated q.having: before %s, after %s", havingBefore, got)
	}
}
