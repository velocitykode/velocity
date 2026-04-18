package orm

import (
	"context"
	"testing"
	"time"
)

// RawScopeUser is a soft-delete model used to exercise the intentional
// scope-bypass behaviour of RawQuery and the opt-in behaviour of
// NewRawQueryWithScopes.
type RawScopeUser struct {
	SoftDeleteModel[RawScopeUser]
	Name string `orm:"column:name"`
}

func (RawScopeUser) TableName() string {
	return "raw_scope_users"
}

func setupRawScopeTest(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	if _, err := m.DB().Exec(`
		CREATE TABLE raw_scope_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			created_at DATETIME,
			updated_at DATETIME,
			deleted_at DATETIME
		)
	`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	now := time.Now()
	if _, err := m.DB().Exec(
		`INSERT INTO raw_scope_users (name, created_at, updated_at, deleted_at) VALUES
			('alive', ?, ?, NULL),
			('ghost', ?, ?, ?)`,
		now, now, now, now, now,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	return m
}

// TestRawQuery_BypassesSoftDeleteScope makes the documented behaviour
// explicit: a raw SELECT over a SoftDeleteModel table returns trashed
// rows alongside live rows. If this test ever starts failing, the
// implementation has started silently applying scope to raw SQL and the
// public contract described in NewRawQuery's godoc has changed.
func TestRawQuery_BypassesSoftDeleteScope(t *testing.T) {
	m := setupRawScopeTest(t)

	rq := NewRawQuery[RawScopeUser]("SELECT id, name, created_at, updated_at, deleted_at FROM raw_scope_users ORDER BY id")
	rq.driver = m.DefaultDriver()

	users, err := rq.Get()
	if err != nil {
		t.Fatalf("rq.Get: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("RawQuery returned %d rows; expected 2 (both live and trashed) — "+
			"scope bypass is part of the documented contract", len(users))
	}
	names := []string{users[0].Name, users[1].Name}
	foundGhost := false
	for _, n := range names {
		if n == "ghost" {
			foundGhost = true
			break
		}
	}
	if !foundGhost {
		t.Errorf("RawQuery did not return trashed row; got %v", names)
	}
}

// TestRawQueryWithScopes_AppliesSoftDeleteScope verifies the opt-in
// helper enforces deleted_at IS NULL on the outer wrapper.
func TestRawQueryWithScopes_AppliesSoftDeleteScope(t *testing.T) {
	m := setupRawScopeTest(t)

	rq := NewRawQueryWithScopes[RawScopeUser]("SELECT id, name, created_at, updated_at, deleted_at FROM raw_scope_users ORDER BY id")
	rq.driver = m.DefaultDriver()

	users, err := rq.Get()
	if err != nil {
		t.Fatalf("rq.Get: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("NewRawQueryWithScopes returned %d rows; want 1 live row", len(users))
	}
	if users[0].Name != "alive" {
		t.Errorf("expected only the live row; got %q", users[0].Name)
	}
}
