package auth

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// newRememberTestDB creates a file-backed sqlite users table that mirrors
// the canonical template schema (integer PK + nullable remember_token).
// A file (not :memory:) is used so every pooled connection sees the same
// table.
func newRememberTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "remember.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		remember_token TEXT
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// TestORMUserProvider_LoadsRememberToken proves the remember-cookie recall
// path can read back the stored token: both load methods must SELECT
// remember_token, otherwise checkRememberCookie always sees an empty hash
// and recall silently never fires.
func TestORMUserProvider_LoadsRememberToken(t *testing.T) {
	db := newRememberTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (name, email, password, remember_token) VALUES (?, ?, ?, ?)`,
		"Ada", "ada@example.com", "hashed-pw", "stored-token-hash",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)
	ctx := context.Background()

	byCreds, err := provider.FindByCredentialsCtx(ctx, map[string]interface{}{"email": "ada@example.com"})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := byCreds.GetRememberToken(); got != "stored-token-hash" {
		t.Errorf("FindByCredentialsCtx remember token = %q, want %q", got, "stored-token-hash")
	}

	byID, err := provider.FindByIDCtx(ctx, byCreds.GetAuthIdentifier())
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := byID.GetRememberToken(); got != "stored-token-hash" {
		t.Errorf("FindByIDCtx remember token = %q, want %q", got, "stored-token-hash")
	}
}

// TestORMUserProvider_NullRememberTokenScansEmpty guards the nullable
// scan: a user that has never set a remember token (NULL column) must
// load cleanly with an empty token, not error.
func TestORMUserProvider_NullRememberTokenScansEmpty(t *testing.T) {
	db := newRememberTestDB(t)
	if _, err := db.Exec(
		`INSERT INTO users (name, email, password) VALUES (?, ?, ?)`,
		"Grace", "grace@example.com", "hashed-pw",
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)
	user, err := provider.FindByIDCtx(context.Background(), 1)
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := user.GetRememberToken(); got != "" {
		t.Errorf("remember token for NULL column = %q, want empty", got)
	}
}
