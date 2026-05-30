package validation

// Tests for the DEPRECATED, orm-free compatibility shims in dbrules_compat.go.
// These prove the reflection-based path reaches a real *sql.DB behind an
// orm.Database and behaves like the dbrules implementation. The orm + driver
// imports are TEST-ONLY, so they do not appear in `go list -deps ./validation`.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/lib/pq"

	"github.com/velocitykode/velocity/orm"
)

func newSQLiteOrm(t *testing.T) orm.Database {
	t.Helper()
	m, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		if strings.Contains(err.Error(), "CGO_ENABLED=0") {
			t.Skipf("skipping SQLite integration: %v", err)
		}
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

func TestCompatUniqueRule_SQLite(t *testing.T) {
	db := newSQLiteOrm(t)
	rule := UniqueRule(db)

	if err := rule("email", "taken@example.com", []string{"users", "email"}, nil); err == nil ||
		!strings.Contains(err.Error(), "already been taken") {
		t.Fatalf("expected 'already been taken', got %v", err)
	}
	if err := rule("email", "fresh@example.com", []string{"users", "email"}, nil); err != nil {
		t.Errorf("expected nil for unused email, got %v", err)
	}
	if err := rule("email", "taken@example.com", []string{"users", "email", "1"}, nil); err != nil {
		t.Errorf("except_id=1 should exclude self, got %v", err)
	}
	if err := rule("email", "x", []string{"users; DROP TABLE users", "email"}, nil); err == nil ||
		!strings.Contains(err.Error(), "invalid SQL identifier") {
		t.Errorf("expected invalid identifier error, got %v", err)
	}
}

func TestCompatExistsRule_SQLite(t *testing.T) {
	db := newSQLiteOrm(t)
	rule := ExistsRule(db)

	if err := rule("team_id", 1, []string{"teams", "id"}, nil); err != nil {
		t.Errorf("expected nil for existing id, got %v", err)
	}
	if err := rule("team_id", 999, []string{"teams", "id"}, nil); err == nil ||
		!strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected 'invalid' for missing id, got %v", err)
	}
}

func TestCompatCheckWithDB_EndToEnd(t *testing.T) {
	db := newSQLiteOrm(t)

	form := url.Values{}
	form.Set("email", "taken@example.com")
	form.Set("team_id", "1")
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	result := CheckWithDB(r, Rules{
		"email":   {"required", "unique:users,email"},
		"team_id": {"required", "exists:teams,id"},
	}, db)

	if !result.HasErrors() {
		t.Fatal("expected unique error on email")
	}
	if result.First("email") == "" {
		t.Error("expected an email error message")
	}
	if result.First("team_id") != "" {
		t.Errorf("team_id exists, want no error, got %q", result.First("team_id"))
	}
}

func TestCompatCheckDataWithDBCtx_NilDB(t *testing.T) {
	// A typed-nil orm.Database passed through the `any` parameter must be
	// detected as nil (dbIsNil guard) so dbHandlers returns no handlers and
	// no reflection call is made on the nil receiver (which would panic).
	// Non-DB rules still validate normally.
	var db orm.Database
	result := CheckDataWithDBCtx(context.Background(), map[string]interface{}{
		"email": "alice@example.com",
	}, Rules{"email": {"required", "email"}}, db)
	if result.HasErrors() {
		t.Fatalf("nil db with non-DB rules should pass, got %v", result.All())
	}
}

func TestCompatAsValidationError_StringMatch(t *testing.T) {
	// Postgres unique violation, single field.
	verr, ok := AsValidationError(errors.New(`pq: duplicate key value (SQLSTATE 23505)`),
		map[string]string{"email": "unique"})
	if !ok || verr == nil || len(verr.Errors["email"]) == 0 {
		t.Fatalf("expected email unique error, got ok=%v verr=%v", ok, verr)
	}

	// SQLite names the column; selectFields should attribute to it.
	verr, ok = AsValidationError(errors.New(`UNIQUE constraint failed: users.email`),
		map[string]string{"email": "unique", "name": "required"})
	if !ok || len(verr.Errors["email"]) == 0 {
		t.Fatalf("expected email attribution, got ok=%v verr=%v", ok, verr)
	}

	// Non-unique error -> not a validation error.
	if _, ok := AsValidationError(errors.New("connection refused"),
		map[string]string{"email": "unique"}); ok {
		t.Error("non-unique error must return ok=false")
	}
}

func TestCompatAsValidationError_RealPqError(t *testing.T) {
	// (*pq.Error).Error() returns "pq: " + Message and omits the SQLSTATE
	// code, so the shim must match pq's canonical unique-violation phrase
	// and recover the column from the quoted constraint name. pq is a
	// TEST-ONLY import here; core stays driver-free.
	err := &pq.Error{
		Code:       "23505",
		Constraint: "users_email_key",
		Message:    `duplicate key value violates unique constraint "users_email_key"`,
	}
	verr, ok := AsValidationError(err, map[string]string{"email": "unique", "name": "required"})
	if !ok || verr == nil || len(verr.Errors["email"]) == 0 {
		t.Fatalf("expected email unique error from real *pq.Error, got ok=%v verr=%v", ok, verr)
	}
	if len(verr.Errors["name"]) != 0 {
		t.Errorf("constraint names email only; name should be unaffected, got %v", verr.Errors["name"])
	}
}
