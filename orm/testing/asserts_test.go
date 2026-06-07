package testing

import (
	"context"
	"fmt"
	"testing"

	"github.com/velocitykode/velocity/orm"
)

// capturingT is a fake TestingT that records whether Errorf was called. It lets
// the failure paths of the assertions be tested without failing the parent test
// (a failed *testing.T subtest would propagate up and fail the whole test).
type capturingT struct {
	helperCalled bool
	errored      bool
	messages     []string
}

func (c *capturingT) Helper() { c.helperCalled = true }

func (c *capturingT) Errorf(format string, args ...interface{}) {
	c.errored = true
	c.messages = append(c.messages, fmt.Sprintf(format, args...))
}

// newAssertManager sets up an in-memory SQLite manager with a posts table that
// has a deleted_at column, plus a few seeded rows.
func newAssertManager(t *testing.T) *orm.Manager {
	t.Helper()

	m, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to create ORM manager: %v", err)
	}
	t.Cleanup(func() { m.Shutdown(context.Background()) })

	ctx := context.Background()
	if _, err := m.Exec(ctx, `CREATE TABLE posts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		title TEXT NOT NULL,
		status TEXT NOT NULL,
		deleted_at TIMESTAMP
	)`); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	rows := []struct {
		title     string
		status    string
		deletedAt any
	}{
		{"alpha", "published", nil},
		{"beta", "published", nil},
		{"gamma", "draft", nil},
		{"delta", "draft", "2026-01-01 00:00:00"},
	}
	for _, r := range rows {
		if _, err := m.Exec(ctx,
			"INSERT INTO posts (title, status, deleted_at) VALUES (?, ?, ?)",
			r.title, r.status, r.deletedAt,
		); err != nil {
			t.Fatalf("failed to insert row: %v", err)
		}
	}

	return m
}

// assertFn is the shared signature of the criteria-based assert helpers.
type assertFn func(TestingT, *orm.Manager, string, map[string]any)

func TestAssertDatabaseCriteria(t *testing.T) {
	m := newAssertManager(t)

	tests := []struct {
		name     string
		fn       assertFn
		criteria map[string]any
		wantPass bool
	}{
		// AssertDatabaseHas
		{"has single column", AssertDatabaseHas, map[string]any{"title": "alpha"}, true},
		{"has multi column", AssertDatabaseHas, map[string]any{"title": "beta", "status": "published"}, true},
		{"has no match", AssertDatabaseHas, map[string]any{"title": "missing"}, false},
		{"has mismatched combo", AssertDatabaseHas, map[string]any{"title": "alpha", "status": "draft"}, false},

		// AssertDatabaseMissing
		{"missing truly absent", AssertDatabaseMissing, map[string]any{"title": "nope"}, true},
		{"missing but present", AssertDatabaseMissing, map[string]any{"title": "alpha"}, false},

		// AssertSoftDeleted
		{"soft deleted present", AssertSoftDeleted, map[string]any{"title": "delta"}, true},
		{"soft deleted absent", AssertSoftDeleted, map[string]any{"title": "alpha"}, false},

		// AssertNotSoftDeleted
		{"not soft deleted present", AssertNotSoftDeleted, map[string]any{"title": "alpha"}, true},
		{"not soft deleted absent", AssertNotSoftDeleted, map[string]any{"title": "delta"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPass {
				// PASS case: a real *testing.T must remain unfailed.
				tt.fn(t, m, "posts", tt.criteria)
				return
			}
			// FAILURE case: a capturing fake records that Errorf fired,
			// without propagating the failure to the parent test.
			fake := &capturingT{}
			tt.fn(fake, m, "posts", tt.criteria)
			if !fake.helperCalled {
				t.Errorf("%s: expected Helper() to be called", tt.name)
			}
			if !fake.errored {
				t.Errorf("%s: expected assertion to fail (Errorf), but it passed", tt.name)
			}
		})
	}
}

func TestAssertDatabaseCount(t *testing.T) {
	m := newAssertManager(t)

	tests := []struct {
		name     string
		expected int
		wantPass bool
	}{
		{"correct count", 4, true},
		{"wrong count low", 2, false},
		{"wrong count high", 10, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantPass {
				AssertDatabaseCount(t, m, "posts", tt.expected)
				return
			}
			fake := &capturingT{}
			AssertDatabaseCount(fake, m, "posts", tt.expected)
			if !fake.errored {
				t.Errorf("%s: expected assertion to fail (Errorf), but it passed", tt.name)
			}
		})
	}
}
