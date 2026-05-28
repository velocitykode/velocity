//go:build integration

// Auth integration tests — run with: make test-integration
//
// The unit-test suite exercises each auth component (bcrypt hasher, JWT
// manager, cookie store, session guard, JWT guard) with mock collaborators.
// This file wires the real components together, backs them with a real
// Postgres user table, and walks the full login → cookie round-trip →
// check → logout flow for both session and JWT guards.
//
// The point of running this "integration" rather than as another unit
// test: real bcrypt verify against rows that were inserted through the
// real SQL driver, real encrypted cookies decoded back by the store that
// wrote them, and real JWT sign → validate crossing the boundary via
// HTTP request headers.
package auth_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/crypto"
)

var authRequiredEnv = []string{
	"POSTGRES_URL", // postgres://user:pass@host:5432/db?sslmode=disable
}

// db is the shared Postgres connection used by all integration tests in
// this package. TestMain opens it once so each test doesn't pay the
// connect cost; it's closed on exit.
var db *sql.DB

func TestMain(m *testing.M) {
	var missing []string
	for _, name := range authRequiredEnv {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr,
			"auth integration tests require env vars (missing: %s) — use `make test-integration`\n",
			strings.Join(missing, ", "))
		os.Exit(1)
	}

	var err error
	db, err = sql.Open("postgres", os.Getenv("POSTGRES_URL"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "sql.Open: %v\n", err)
		os.Exit(1)
	}
	if err := db.Ping(); err != nil {
		fmt.Fprintf(os.Stderr, "db.Ping: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	os.Exit(m.Run())
}

// pgUserProvider is a minimal auth.UserProvider backed by a Postgres table.
// Each test creates its own table so parallel runs don't clobber each other.
type pgUserProvider struct {
	db     *sql.DB
	table  string
	hasher auth.Hasher
}

func (p *pgUserProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	return p.FindByIDCtx(context.Background(), id)
}

func (p *pgUserProvider) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	q := fmt.Sprintf("SELECT id, email, password, remember_token FROM %s WHERE id=$1", p.table)
	row := p.db.QueryRowContext(ctx, q, id)
	u := &auth.AuthUser{}
	var remember sql.NullString
	if err := row.Scan(&u.ID, &u.Email, &u.Password, &remember); err != nil {
		if err == sql.ErrNoRows {
			return nil, auth.ErrUserNotFound
		}
		return nil, err
	}
	u.RememberToken = remember.String
	return u, nil
}

func (p *pgUserProvider) FindByCredentials(credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentialsCtx(context.Background(), credentials)
}

func (p *pgUserProvider) FindByCredentialsCtx(ctx context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	email, _ := credentials["email"].(string)
	q := fmt.Sprintf("SELECT id, email, password, remember_token FROM %s WHERE email=$1", p.table)
	row := p.db.QueryRowContext(ctx, q, email)
	u := &auth.AuthUser{}
	var remember sql.NullString
	if err := row.Scan(&u.ID, &u.Email, &u.Password, &remember); err != nil {
		if err == sql.ErrNoRows {
			return nil, auth.ErrUserNotFound
		}
		return nil, err
	}
	u.RememberToken = remember.String
	return u, nil
}

func (p *pgUserProvider) ValidateCredentials(user auth.Authenticatable, credentials map[string]interface{}) bool {
	password, _ := credentials["password"].(string)
	return p.hasher.Verify(password, user.GetAuthPassword())
}

func (p *pgUserProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	return p.UpdateRememberTokenCtx(context.Background(), user, token)
}

func (p *pgUserProvider) UpdateRememberTokenCtx(ctx context.Context, user auth.Authenticatable, token string) error {
	q := fmt.Sprintf("UPDATE %s SET remember_token=$1 WHERE id=$2", p.table)
	_, err := p.db.ExecContext(ctx, q, token, user.GetAuthIdentifier())
	return err
}

// setupUsersTable creates a fresh table with a unique name per test and
// seeds one user. Returns the provider and the plaintext password so the
// test can attempt login with known-good creds.
func setupUsersTable(t *testing.T) (*pgUserProvider, string) {
	t.Helper()

	table := fmt.Sprintf("auth_integration_%d_%d", os.Getpid(), time.Now().UnixNano())
	ddl := fmt.Sprintf(`CREATE TABLE %s (
		id SERIAL PRIMARY KEY,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		remember_token TEXT
	)`, table)
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", table))
	})

	hasher := auth.NewBcryptHasher(4) // low cost so the test is fast
	password := "correct horse battery staple"
	hashed, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if _, err := db.Exec(
		fmt.Sprintf("INSERT INTO %s (email, password) VALUES ($1, $2)", table),
		"alice@example.com", hashed,
	); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	return &pgUserProvider{db: db, table: table, hasher: hasher}, password
}

// TestSessionGuard_LoginThenCheckThenLogout walks the full cookie flow:
// Login issues a Set-Cookie, a subsequent request carrying that cookie is
// Check-authenticated, and Logout rejects the same cookie on the round
// after.
func TestSessionGuard_LoginThenCheckThenLogout(t *testing.T) {
	provider, password := setupUsersTable(t)

	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32), // 32 raw bytes → AES-256
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	guard, err := guards.NewSessionGuard(provider, auth.SessionConfig{
		Name:     "velocity_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}

	// 1. Attempt with good credentials — guard writes a session cookie.
	loginW := httptest.NewRecorder()
	loginR := httptest.NewRequest("POST", "/login", nil)
	loginR = guards.WithSessionContext(loginR)
	ok, err := guard.Attempt(loginW, loginR, map[string]interface{}{
		"email":    "alice@example.com",
		"password": password,
	})
	if err != nil || !ok {
		t.Fatalf("Attempt(good): ok=%v err=%v", ok, err)
	}

	cookies := loginW.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == "velocity_session" {
			session = c
			break
		}
	}
	if session == nil {
		t.Fatalf("expected Set-Cookie for velocity_session; got %+v", cookies)
	}

	// 2. Subsequent request carrying the cookie must Check-pass.
	checkR := httptest.NewRequest("GET", "/dashboard", nil)
	checkR.AddCookie(session)
	checkR = guards.WithSessionContext(checkR)
	if !guard.Check(checkR) {
		t.Fatal("Check must return true for a request with a freshly issued session cookie")
	}

	user := guard.User(checkR)
	if user == nil {
		t.Fatal("User must return the authenticated user")
	}
	if user.(*auth.AuthUser).Email != "alice@example.com" {
		t.Errorf("user email = %q, want alice@example.com", user.(*auth.AuthUser).Email)
	}

	// 3. Logout invalidates the session; the same cookie no longer checks.
	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest("POST", "/logout", nil)
	logoutR.AddCookie(session)
	logoutR = guards.WithSessionContext(logoutR)
	if err := guard.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	postLogoutR := httptest.NewRequest("GET", "/dashboard", nil)
	postLogoutR.AddCookie(session)
	postLogoutR = guards.WithSessionContext(postLogoutR)
	if guard.Check(postLogoutR) {
		t.Error("Check must return false after Logout destroys the session")
	}
}

// TestSessionGuard_BadCredentialsRejected verifies the guard does not
// issue a session cookie on a wrong password — a regression where
// Attempt returned ok=true on a wrong password would silently log
// anyone in.
func TestSessionGuard_BadCredentialsRejected(t *testing.T) {
	provider, _ := setupUsersTable(t)

	enc, _ := crypto.NewEncryptor(crypto.Config{
		Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM",
	})
	guard, err := guards.NewSessionGuard(provider, auth.SessionConfig{
		Name: "velocity_session", Lifetime: 60, Path: "/",
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r = guards.WithSessionContext(r)
	ok, _ := guard.Attempt(w, r, map[string]interface{}{
		"email":    "alice@example.com",
		"password": "wrong password",
	})
	if ok {
		t.Fatal("Attempt(wrong password) returned ok=true")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "velocity_session" && c.Value != "" {
			t.Errorf("bad login must not issue a non-empty session cookie; got %q", c.Value)
		}
	}
}

// TestJWTGuard_LoginValidateLogout exercises the JWT flow including the
// blacklist: after Logout, the same token must no longer validate.
// Without the blacklist this is trivially defeated; with it, the guard
// stores the token's JTI and rejects it on re-presentation.
func TestJWTGuard_LoginValidateLogout(t *testing.T) {
	provider, password := setupUsersTable(t)

	secret := strings.Repeat("s", 48)
	guard, err := guards.NewJWTGuard(provider, auth.JWTConfig{
		Secret:           secret,
		Algorithm:        "HS256",
		TTL:              5,
		RefreshTTL:       60,
		BlacklistEnabled: true,
		BlacklistStore:   auth.NewInMemoryBlacklistStore(),
	})
	if err != nil {
		t.Fatalf("NewJWTGuard: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	guard.Start(ctx)
	t.Cleanup(cancel)

	// 1. Login via credentials — Attempt writes the token into a header.
	loginW := httptest.NewRecorder()
	loginR := httptest.NewRequest("POST", "/api/login", nil)
	ok, err := guard.Attempt(loginW, loginR, map[string]interface{}{
		"email":    "alice@example.com",
		"password": password,
	})
	if err != nil || !ok {
		t.Fatalf("Attempt: ok=%v err=%v", ok, err)
	}

	token := loginW.Header().Get("X-Auth-Token")
	if token == "" {
		t.Fatalf("Attempt must set X-Auth-Token header")
	}

	// The guard only caches users when Check/User is called. Manually
	// ValidateToken asserts the token is structurally correct before we
	// carry it to the protected request.
	if _, err := guard.ValidateToken(token); err != nil {
		t.Fatalf("ValidateToken(fresh): %v", err)
	}

	// 2. Carry the token on a subsequent request — Check must pass.
	protectedR := httptest.NewRequest("GET", "/api/me", nil)
	protectedR.Header.Set("Authorization", "Bearer "+token)
	if !guard.Check(protectedR) {
		t.Fatal("Check must return true for a fresh JWT")
	}

	// 3. Logout revokes the token via its JTI.
	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest("POST", "/api/logout", nil)
	logoutR.Header.Set("Authorization", "Bearer "+token)
	if err := guard.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// 4. The same token must no longer validate (blacklist hit).
	if _, err := guard.ValidateToken(token); err == nil {
		t.Error("ValidateToken must error on a revoked token; blacklist missed")
	}
	postLogoutR := httptest.NewRequest("GET", "/api/me", nil)
	postLogoutR.Header.Set("Authorization", "Bearer "+token)
	if guard.Check(postLogoutR) {
		t.Error("Check must return false after Logout blacklists the token")
	}
}

// TestSessionGuard_TamperedCookieRejected surfaces a class of real
// breach: an attacker flips bits in the encrypted session cookie. The
// store's AEAD must reject the payload — Check must return false, and
// no user is ever surfaced.
func TestSessionGuard_TamperedCookieRejected(t *testing.T) {
	provider, password := setupUsersTable(t)

	enc, _ := crypto.NewEncryptor(crypto.Config{
		Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM",
	})
	guard, err := guards.NewSessionGuard(provider, auth.SessionConfig{
		Name: "velocity_session", Lifetime: 60, Path: "/",
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}

	loginW := httptest.NewRecorder()
	loginR := guards.WithSessionContext(httptest.NewRequest("POST", "/login", nil))
	if _, err := guard.Attempt(loginW, loginR, map[string]interface{}{
		"email":    "alice@example.com",
		"password": password,
	}); err != nil {
		t.Fatalf("Attempt: %v", err)
	}

	var session *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == "velocity_session" {
			session = c
			break
		}
	}
	if session == nil {
		t.Fatal("no session cookie issued")
	}

	// Tamper with the ciphertext: decode, flip one byte, re-encode.
	raw, err := base64.StdEncoding.DecodeString(session.Value)
	if err != nil {
		// Not every store uses std base64 padding — try URL encoding.
		raw, err = base64.URLEncoding.DecodeString(session.Value)
		if err != nil {
			t.Fatalf("decode session cookie: %v (value=%q)", err, session.Value)
		}
	}
	if len(raw) == 0 {
		t.Fatal("session cookie value is empty")
	}
	raw[len(raw)/2] ^= 0xFF
	tampered := &http.Cookie{
		Name:  session.Name,
		Value: base64.StdEncoding.EncodeToString(raw),
	}

	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.AddCookie(tampered)
	r = guards.WithSessionContext(r)
	if guard.Check(r) {
		t.Error("Check must reject a tampered session cookie")
	}
	if u := guard.User(r); u != nil {
		t.Errorf("User must be nil for a tampered cookie, got %v", u)
	}
}
