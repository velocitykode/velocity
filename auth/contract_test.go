package auth_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"golang.org/x/crypto/bcrypt"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
)

const (
	contractTestEmail    = "alice@example.com"
	contractTestPassword = "correct-horse-battery-staple"
	contractTestName     = "Alice"
)

// newContractAuthDB spins up a per-test SQLite database with the users
// table the ORMUserProvider expects and seeds one user.
func newContractAuthDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + t.TempDir() + "/auth-contract.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		remember_token TEXT
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(contractTestPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (name, email, password) VALUES (?, ?, ?)`,
		contractTestName, contractTestEmail, string(hash)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

// TestORMUserProvider_Contract runs the authtest spec against the SQL-backed
// user provider.
func TestORMUserProvider_Contract(t *testing.T) {
	authtest.RunUserProviderContractTests(t, authtest.UserProviderFactory{
		New: func(t *testing.T) auth.UserProvider {
			db := newContractAuthDB(t)
			return auth.NewORMUserProvider(db, "users", auth.NewBcryptHasher(bcrypt.MinCost))
		},
		SeedUser:     &auth.AuthUser{ID: uint(1), Email: contractTestEmail, Name: contractTestName},
		SeedEmail:    contractTestEmail,
		SeedPassword: contractTestPassword,
	})
}

// TestMemorySessionStore_Contract runs the authtest spec against the
// in-process ServerSessionStore.
func TestMemorySessionStore_Contract(t *testing.T) {
	authtest.RunServerSessionStoreContractTests(t, func(t *testing.T) auth.ServerSessionStore {
		return session.NewMemoryStore()
	})
}

// TestNoopLoginThrottler_Contract runs the authtest spec against the
// default no-op throttler.
func TestNoopLoginThrottler_Contract(t *testing.T) {
	authtest.RunLoginThrottlerContractTests(t, func(t *testing.T) contract.LoginThrottler {
		return auth.NoopLoginThrottler{}
	})
}
