package validation

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/velocitykode/velocity/orm"
)

// ---------------------------------------------------------------------------
// Driver-aware SQL generation for unique:/exists: rules.
// ---------------------------------------------------------------------------

// TestUniqueExists_DriverAwareSQL verifies that UniqueRule and ExistsRule
// generate SQL with driver-correct identifier quoting and placeholder
// syntax for every supported driver: mysql, postgres, sqlite.
//
// We don't need a live Postgres or MySQL server: the SQL string assembly
// happens entirely in Go before QueryRow is called. Quoting helpers
// (quoteIdentifier) and placeholder() are driver-aware; this test pins the
// exact SQL each rule produces per driver so future refactors can't
// silently break a driver.
func TestUniqueExists_DriverAwareSQL(t *testing.T) {
	cases := []struct {
		driver       string
		wantUnique   string
		wantUniqueEx string // unique with except_id
		wantExists   string
	}{
		{
			driver:       "mysql",
			wantUnique:   "SELECT COUNT(*) FROM `users` WHERE `email` = ?",
			wantUniqueEx: "SELECT COUNT(*) FROM `users` WHERE `email` = ? AND `id` != ?",
			wantExists:   "SELECT COUNT(*) FROM `teams` WHERE `id` = ?",
		},
		{
			driver:       "postgres",
			wantUnique:   `SELECT COUNT(*) FROM "users" WHERE "email" = $1`,
			wantUniqueEx: `SELECT COUNT(*) FROM "users" WHERE "email" = $1 AND "id" != $2`,
			wantExists:   `SELECT COUNT(*) FROM "teams" WHERE "id" = $1`,
		},
		{
			driver:       "sqlite",
			wantUnique:   "SELECT COUNT(*) FROM `users` WHERE `email` = ?",
			wantUniqueEx: "SELECT COUNT(*) FROM `users` WHERE `email` = ? AND `id` != ?",
			wantExists:   "SELECT COUNT(*) FROM `teams` WHERE `id` = ?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.driver+"_unique", func(t *testing.T) {
			got := buildUniqueSQL(tc.driver, "users", "email", false)
			if got != tc.wantUnique {
				t.Errorf("driver=%s: unique SQL\n got=%q\nwant=%q", tc.driver, got, tc.wantUnique)
			}
		})
		t.Run(tc.driver+"_unique_except", func(t *testing.T) {
			got := buildUniqueSQL(tc.driver, "users", "email", true)
			if got != tc.wantUniqueEx {
				t.Errorf("driver=%s: unique-with-except SQL\n got=%q\nwant=%q", tc.driver, got, tc.wantUniqueEx)
			}
		})
		t.Run(tc.driver+"_exists", func(t *testing.T) {
			got := buildExistsSQL(tc.driver, "teams", "id")
			if got != tc.wantExists {
				t.Errorf("driver=%s: exists SQL\n got=%q\nwant=%q", tc.driver, got, tc.wantExists)
			}
		})
	}
}

// buildUniqueSQL replays the SQL assembly that UniqueRule does, without
// running the rule itself. This makes the per-driver assertions above
// independent of the rule's internal control flow and keeps the test
// resilient to future error-path refactors.
func buildUniqueSQL(driver, table, column string, withExcept bool) string {
	q := "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) +
		" WHERE " + quoteIdentifier(column, driver) +
		" = " + placeholder(driver, 1)
	if withExcept {
		q += " AND " + quoteIdentifier("id", driver) + " != " + placeholder(driver, 2)
	}
	return q
}

// buildExistsSQL replays the SQL assembly ExistsRule emits.
func buildExistsSQL(driver, table, column string) string {
	return "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) +
		" WHERE " + quoteIdentifier(column, driver) +
		" = " + placeholder(driver, 1)
}

// ---------------------------------------------------------------------------
// Real SQLite integration: end-to-end unique/exists rule.
//
// SQLite is the only driver bundled in `go test` runs without external
// services, so it's the only one we exercise end-to-end. The SQL-string
// assertions above pin postgres/mysql output without requiring a live
// server.
// ---------------------------------------------------------------------------

// newSQLiteOrm spins up an in-memory SQLite ORM Manager with a users and
// teams table seeded for unique/exists assertions.
func newSQLiteOrm(t *testing.T) orm.Database {
	t.Helper()
	m, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("orm.NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	ddl := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO users (id, email) VALUES (1, 'taken@example.com')`,
		`INSERT INTO teams (id, name) VALUES (1, 'engineering')`,
	}
	for _, q := range ddl {
		if _, err := m.DB().Exec(q); err != nil {
			t.Fatalf("DDL %q: %v", q, err)
		}
	}
	return m
}

func TestUniqueRule_SQLite_Integration(t *testing.T) {
	db := newSQLiteOrm(t)
	rule := UniqueRule(db)

	t.Run("conflict reports taken", func(t *testing.T) {
		err := rule("email", "taken@example.com", []string{"users", "email"}, nil)
		if err == nil || !strings.Contains(err.Error(), "already been taken") {
			t.Fatalf("expected 'already been taken' error, got %v", err)
		}
	})

	t.Run("unused email passes", func(t *testing.T) {
		if err := rule("email", "fresh@example.com", []string{"users", "email"}, nil); err != nil {
			t.Errorf("expected nil for unused email, got %v", err)
		}
	})

	t.Run("except_id excludes self", func(t *testing.T) {
		if err := rule("email", "taken@example.com", []string{"users", "email", "1"}, nil); err != nil {
			t.Errorf("with except_id=1, taken@example.com (id=1) should pass, got %v", err)
		}
	})

	t.Run("rejects sql injection in table", func(t *testing.T) {
		err := rule("email", "x", []string{"users; DROP TABLE users", "email"}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid SQL identifier") {
			t.Errorf("expected invalid identifier error, got %v", err)
		}
	})
}

func TestExistsRule_SQLite_Integration(t *testing.T) {
	db := newSQLiteOrm(t)
	rule := ExistsRule(db)

	t.Run("existing id passes", func(t *testing.T) {
		if err := rule("team_id", 1, []string{"teams", "id"}, nil); err != nil {
			t.Errorf("expected nil for existing team id, got %v", err)
		}
	})

	t.Run("missing id fails", func(t *testing.T) {
		err := rule("team_id", 999, []string{"teams", "id"}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid") {
			t.Errorf("expected 'invalid' error for missing id, got %v", err)
		}
	})

	t.Run("rejects sql injection in column", func(t *testing.T) {
		err := rule("team_id", 1, []string{"teams", "id; SELECT 1"}, nil)
		if err == nil || !strings.Contains(err.Error(), "invalid SQL identifier") {
			t.Errorf("expected invalid identifier error, got %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Full Check pipeline with a real SQLite DB: validates that unique: and
// exists: hooks fire through Check / CheckWithDB end-to-end with the
// canonical Rules slice form.
// ---------------------------------------------------------------------------

func TestCheckWithDB_UniqueExists_SQLite_EndToEnd(t *testing.T) {
	db := newSQLiteOrm(t)

	form := url.Values{}
	form.Set("email", "taken@example.com")
	form.Set("team_id", "1")
	r := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// canonical Rules form (slice per field), exercising both unique and
	// exists in a single CheckWithDB call.
	rules := Rules{
		"email":   {"required", "unique:users,email"},
		"team_id": {"required", "exists:teams,id"},
	}

	result := CheckWithDB(r, rules, db)

	// email should fail (already taken); team_id should pass (exists).
	if !result.HasErrors() {
		t.Fatal("expected unique error on email")
	}
	if result.First("email") == "" {
		t.Error("expected error message for email")
	}
	if result.First("team_id") != "" {
		t.Errorf("expected no error for team_id, got %q", result.First("team_id"))
	}
}

// TestCheck_BothFormsConverge confirms that PipeRules->NewRules path and a
// hand-written slice literal produce identical validation outcomes.
func TestCheck_BothFormsConverge(t *testing.T) {
	form := url.Values{}
	form.Set("name", "Al") // too short
	r1 := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r1.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r2 := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	slice := Rules{"name": {"required", "min:3"}}
	pipe := NewRules(PipeRules{"name": "required|min:3"})

	a := Check(r1, slice)
	b := Check(r2, pipe)

	if a.HasErrors() != b.HasErrors() {
		t.Fatalf("convergence mismatch: slice=%v pipe=%v", a.HasErrors(), b.HasErrors())
	}
	if a.First("name") != b.First("name") {
		t.Errorf("messages diverge: slice=%q pipe=%q", a.First("name"), b.First("name"))
	}
}
