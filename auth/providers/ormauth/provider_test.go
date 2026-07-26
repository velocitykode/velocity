package ormauth_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/authtest"
	"github.com/velocitykode/velocity/auth/providers/ormauth"
	"github.com/velocitykode/velocity/orm"
)

const (
	testEmail    = "alice@example.com"
	testPassword = "correct-horse-battery-staple"
	testName     = "Alice"
)

// usersSchema mirrors the canonical template schema: integer PK, nullable
// remember_token.
const usersSchema = `CREATE TABLE users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL UNIQUE,
	password TEXT NOT NULL,
	remember_token TEXT
)`

// newManager spins up a per-test file-backed SQLite manager and installs it
// as the ORM default, which is where orm.Model[T] resolves its connection
// from. A file (not :memory:) is used so every pooled connection sees the
// same tables.
func newManager(t *testing.T) *orm.Manager {
	t.Helper()

	m, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: filepath.Join(t.TempDir(), "ormauth.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	orm.SetDefault(m)
	t.Cleanup(orm.ResetDefault)

	return m
}

// seedUser inserts one user and returns the bcrypt hash it stored.
func seedUser(t *testing.T, m *orm.Manager, email, password string) string {
	t.Helper()

	if _, err := m.DB().Exec(usersSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if _, err := m.DB().Exec(
		`INSERT INTO users (name, email, password) VALUES (?, ?, ?)`,
		testName, email, string(hash),
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return string(hash)
}

// newProvider builds the framework default-model provider with a cheap
// hasher, the same construction velocity.New performs.
func newProvider(t *testing.T) auth.UserProvider {
	t.Helper()

	p := ormauth.New[ormauth.User](ormauth.WithHasher(auth.NewBcryptHasher(bcrypt.MinCost)))
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return p
}

// TestProvider_Contract runs the authtest executable specification against
// the ORM-backed provider.
func TestProvider_Contract(t *testing.T) {
	authtest.RunUserProviderContractTests(t, authtest.UserProviderFactory{
		New: func(t *testing.T) auth.UserProvider {
			m := newManager(t)
			seedUser(t, m, testEmail, testPassword)
			return newProvider(t)
		},
		SeedUser:     &auth.AuthUser{ID: uint(1), Email: testEmail, Name: testName},
		SeedEmail:    testEmail,
		SeedPassword: testPassword,
	})
}

// TestProvider_QueriesTheModelsTable proves the provider reads the table
// derived from the registered model type rather than a hardcoded name, and
// that both lookups return the same row.
func TestProvider_QueriesTheModelsTable(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	p := newProvider(t)
	ctx := context.Background()

	byCreds, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := byCreds.GetAuthIdentifier(); got != uint(1) {
		t.Fatalf("identifier = %#v, want uint(1)", got)
	}

	byID, err := p.FindByIDCtx(ctx, byCreds.GetAuthIdentifier())
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if byID.GetAuthPassword() != byCreds.GetAuthPassword() {
		t.Error("the two lookups returned different rows")
	}
}

// TestProvider_LoadsRememberToken proves the remember-cookie recall path can
// read the stored token back: if a load path dropped the column,
// checkRememberCookie would always see an empty hash and recall would
// silently never fire.
func TestProvider_LoadsRememberToken(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	if _, err := m.DB().Exec(`UPDATE users SET remember_token = ? WHERE id = 1`, "stored-token-hash"); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	p := newProvider(t)
	ctx := context.Background()

	byCreds, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if got := byCreds.GetRememberToken(); got != "stored-token-hash" {
		t.Errorf("FindByCredentialsCtx remember token = %q, want %q", got, "stored-token-hash")
	}

	byID, err := p.FindByIDCtx(ctx, byCreds.GetAuthIdentifier())
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := byID.GetRememberToken(); got != "stored-token-hash" {
		t.Errorf("FindByIDCtx remember token = %q, want %q", got, "stored-token-hash")
	}
}

// TestProvider_NullRememberTokenLoadsEmpty guards the nullable column: a
// user who has never used remember-me must load cleanly with an empty
// token, not fail the scan.
func TestProvider_NullRememberTokenLoadsEmpty(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)

	user, err := newProvider(t).FindByIDCtx(context.Background(), 1)
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if got := user.GetRememberToken(); got != "" {
		t.Errorf("remember token for NULL column = %q, want empty", got)
	}
}

// TestProvider_UpdateRememberTokenPersists covers the login-path write.
func TestProvider_UpdateRememberTokenPersists(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	p := newProvider(t)
	ctx := context.Background()

	user, err := p.FindByIDCtx(ctx, 1)
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if err := p.UpdateRememberTokenCtx(ctx, user, "fresh-token"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}
	if got := user.GetRememberToken(); got != "fresh-token" {
		t.Errorf("in-memory token = %q, want %q", got, "fresh-token")
	}

	var stored string
	if err := m.DB().QueryRow(`SELECT remember_token FROM users WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "fresh-token" {
		t.Errorf("persisted token = %q, want %q", stored, "fresh-token")
	}
}

// TestProvider_CompareAndSwapRememberToken pins the rotate-on-use
// contract: the swap lands only when the row still holds the old token.
func TestProvider_CompareAndSwapRememberToken(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)

	p := newProvider(t)
	swapper, ok := p.(auth.RememberTokenCompareAndSwapper)
	if !ok {
		t.Fatal("provider does not implement RememberTokenCompareAndSwapper; remember-me recall would fail closed")
	}
	ctx := context.Background()

	user, err := p.FindByIDCtx(ctx, 1)
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if err := p.UpdateRememberTokenCtx(ctx, user, "token-1"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}

	swapped, err := swapper.CompareAndSwapRememberToken(ctx, user, "token-1", "token-2")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("swap with the current token reported false")
	}
	if got := user.GetRememberToken(); got != "token-2" {
		t.Errorf("in-memory token after swap = %q, want token-2", got)
	}

	// A second presentation of the now-stale token must lose, without
	// error and without mutating the in-memory user.
	swapped, err = swapper.CompareAndSwapRememberToken(ctx, user, "token-1", "token-3")
	if err != nil {
		t.Fatalf("stale swap returned an error: %v", err)
	}
	if swapped {
		t.Fatal("stale token swapped; two parallel recalls could both mint a credential")
	}
	if got := user.GetRememberToken(); got != "token-2" {
		t.Errorf("in-memory token mutated on a losing swap: %q", got)
	}

	var stored string
	if err := m.DB().QueryRow(`SELECT remember_token FROM users WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "token-2" {
		t.Errorf("persisted token = %q, want token-2", stored)
	}
}

// TestProvider_CompareAndSwapRememberToken_Concurrent proves only one of N
// racing recalls of the same cookie can win.
func TestProvider_CompareAndSwapRememberToken_Concurrent(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)

	p := newProvider(t)
	swapper := p.(auth.RememberTokenCompareAndSwapper)
	ctx := context.Background()

	user, err := p.FindByIDCtx(ctx, 1)
	if err != nil {
		t.Fatalf("FindByIDCtx: %v", err)
	}
	if err := p.UpdateRememberTokenCtx(ctx, user, "shared"); err != nil {
		t.Fatalf("UpdateRememberTokenCtx: %v", err)
	}

	const racers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each racer carries its own in-memory user so the
			// assertion measures the database swap, not a data race
			// on one shared struct.
			u, err := p.FindByIDCtx(ctx, 1)
			if err != nil {
				return
			}
			ok, err := swapper.CompareAndSwapRememberToken(ctx, u, "shared", "winner")
			if err != nil || !ok {
				return
			}
			mu.Lock()
			wins++
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("%d racers won the swap, want exactly 1", wins)
	}
}

// TestProvider_UnknownRowsReturnErrUserNotFound covers both miss paths.
func TestProvider_UnknownRowsReturnErrUserNotFound(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	p := newProvider(t)
	ctx := context.Background()

	if _, err := p.FindByIDCtx(ctx, 9999); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("FindByIDCtx unknown id: got %v, want ErrUserNotFound", err)
	}
	if _, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{"email": "ghost@example.com"}); !errors.Is(err, auth.ErrUserNotFound) {
		t.Errorf("FindByCredentialsCtx unknown email: got %v, want ErrUserNotFound", err)
	}
	if _, err := p.FindByCredentialsCtx(ctx, map[string]interface{}{}); err == nil {
		t.Error("missing credential key should error")
	}
}

// TestProvider_DeprecatedShims covers the non-Ctx variants the UserProvider
// interface still carries.
func TestProvider_DeprecatedShims(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	p := newProvider(t)

	user, err := p.FindByCredentials(map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentials: %v", err)
	}
	if _, err := p.FindByID(user.GetAuthIdentifier()); err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if err := p.UpdateRememberToken(user, "shim-token"); err != nil {
		t.Fatalf("UpdateRememberToken: %v", err)
	}

	var stored string
	if err := m.DB().QueryRow(`SELECT remember_token FROM users WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != "shim-token" {
		t.Errorf("persisted token = %q, want shim-token", stored)
	}
}

// TestProvider_ValidateCredentials covers the pure-CPU comparison,
// including the shapes that must collapse to false rather than panic.
func TestProvider_ValidateCredentials(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	p := newProvider(t)

	user, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}

	if !p.ValidateCredentials(user, map[string]interface{}{"password": testPassword}) {
		t.Error("correct password rejected")
	}
	if p.ValidateCredentials(user, map[string]interface{}{"password": "wrong"}) {
		t.Error("wrong password accepted")
	}
	if p.ValidateCredentials(user, map[string]interface{}{"password": 1234}) {
		t.Error("non-string password accepted")
	}
	if p.ValidateCredentials(nil, map[string]interface{}{"password": testPassword}) {
		t.Error("nil user accepted")
	}
}

// TestProvider_UsesConfiguredHasher proves the framework-supplied hasher
// (and therefore the operator's configured bcrypt cost) is the one used to
// verify, rather than a provider-local default.
func TestProvider_UsesConfiguredHasher(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)

	var asked []string
	spy := &spyHasher{verify: func(password, hash string) bool {
		asked = append(asked, password)
		return password == testPassword
	}}

	p := ormauth.New[ormauth.User](ormauth.WithHasher(spy))
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	user, err := p.FindByCredentialsCtx(context.Background(), map[string]interface{}{"email": testEmail})
	if err != nil {
		t.Fatalf("FindByCredentialsCtx: %v", err)
	}
	if !p.ValidateCredentials(user, map[string]interface{}{"password": testPassword}) {
		t.Fatal("spy hasher should have accepted the seeded password")
	}
	if len(asked) != 1 {
		t.Fatalf("configured hasher consulted %d times, want 1", len(asked))
	}
}

// spyHasher records Verify calls.
type spyHasher struct {
	verify func(password, hash string) bool
}

func (s *spyHasher) Hash(string) (string, error)       { return "", nil }
func (s *spyHasher) Verify(password, hash string) bool { return s.verify(password, hash) }
func (s *spyHasher) NeedsRehash(string) bool           { return false }
