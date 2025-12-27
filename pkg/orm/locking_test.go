package orm

import (
	"testing"

	"github.com/velocitykode/velocity/pkg/orm/drivers"
)

func TestLockingMethods(t *testing.T) {
	// Create a test query
	q := newQuery[TestModel]()

	t.Run("LockForUpdate", func(t *testing.T) {
		q.LockForUpdate()
		if !q.lockForUpdate {
			t.Error("LockForUpdate should set lockForUpdate to true")
		}
	})

	t.Run("SkipLocked", func(t *testing.T) {
		q.SkipLocked()
		if !q.skipLocked {
			t.Error("SkipLocked should set skipLocked to true")
		}
	})

	t.Run("Chaining", func(t *testing.T) {
		q2 := newQuery[TestModel]()
		q2.Where("status = ?", "pending").
			LockForUpdate().
			SkipLocked().
			Limit(10)

		if !q2.lockForUpdate {
			t.Error("LockForUpdate should be set in chain")
		}
		if !q2.skipLocked {
			t.Error("SkipLocked should be set in chain")
		}
		if q2.limit == nil || *q2.limit != 10 {
			t.Error("Limit should be set in chain")
		}
	})
}

func TestLockingInSQL(t *testing.T) {
	t.Run("PostgreSQL", func(t *testing.T) {
		grammar := &drivers.PostgresGrammar{}

		selectQuery := &drivers.SelectQuery{
			Table:         "jobs",
			Columns:       []string{"*"},
			LockForUpdate: true,
			SkipLocked:    true,
		}

		sql, _ := grammar.CompileSelect(selectQuery)

		// Check if SQL contains locking clauses
		expectedSuffix := " FOR UPDATE SKIP LOCKED"
		if len(sql) < len(expectedSuffix) {
			t.Fatalf("SQL too short: %s", sql)
		}

		if sql[len(sql)-len(expectedSuffix):] != expectedSuffix {
			t.Errorf("SQL should end with '%s', got: %s", expectedSuffix, sql)
		}
	})

	t.Run("PostgreSQL without SKIP LOCKED", func(t *testing.T) {
		grammar := &drivers.PostgresGrammar{}

		selectQuery := &drivers.SelectQuery{
			Table:         "jobs",
			Columns:       []string{"*"},
			LockForUpdate: true,
			SkipLocked:    false,
		}

		sql, _ := grammar.CompileSelect(selectQuery)

		// Check if SQL contains FOR UPDATE but not SKIP LOCKED
		expectedSuffix := " FOR UPDATE"
		if len(sql) < len(expectedSuffix) {
			t.Fatalf("SQL too short: %s", sql)
		}

		actualSuffix := sql[len(sql)-len(expectedSuffix):]
		if actualSuffix != expectedSuffix {
			t.Errorf("SQL should end with '%s', got suffix: %s", expectedSuffix, actualSuffix)
		}
	})

	t.Run("SQLite ignores locking", func(t *testing.T) {
		grammar := &drivers.SQLiteGrammar{}

		selectQuery := &drivers.SelectQuery{
			Table:         "jobs",
			Columns:       []string{"*"},
			LockForUpdate: true,
			SkipLocked:    true,
		}

		sql, _ := grammar.CompileSelect(selectQuery)

		// SQLite should not have FOR UPDATE
		if len(sql) > 0 && sql[len(sql)-1] == 'D' {
			// Check it doesn't end with LOCKED
			if len(sql) >= 6 && sql[len(sql)-6:] == "LOCKED" {
				t.Error("SQLite should not include LOCKED clause")
			}
		}
	})
}

// TestModel for testing
type TestModel struct {
	ID     uint
	Name   string
	Status string
}