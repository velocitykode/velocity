package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"golang.org/x/crypto/bcrypt"
)

func TestPasswordHashing(t *testing.T) {
	hasher := NewBcryptHasher(10)

	// Test hashing
	password := "my-secure-password"
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	// Hash should not be empty
	if hash == "" {
		t.Error("Hash should not be empty")
	}

	// Hash should not equal password
	if hash == password {
		t.Error("Hash should not equal plain password")
	}

	// Test verification with correct password
	if !hasher.Verify(password, hash) {
		t.Error("Should verify correct password")
	}

	// Test verification with wrong password
	if hasher.Verify("wrong-password", hash) {
		t.Error("Should not verify wrong password")
	}

	// Test empty password
	_, err = hasher.Hash("")
	if err == nil {
		t.Error("Should error on empty password")
	}

	// Test NeedsRehash — use cost 12 for "current" and cost 10 (minimum secure) for "old"
	currentHasher := NewBcryptHasher(12)
	currentHash, _ := currentHasher.Hash(password)
	oldHasher := NewBcryptHasher(10)
	oldHash, _ := oldHasher.Hash(password)

	if !currentHasher.NeedsRehash(oldHash) {
		t.Error("Should need rehash for lower cost")
	}

	if currentHasher.NeedsRehash(currentHash) {
		t.Error("Should not need rehash for same cost")
	}
}

func TestJWTGeneration(t *testing.T) {
	config := JWTConfig{
		Secret:    "test-secret-key-for-testing-must-be-32",
		Algorithm: "HS256",
		TTL:       60,
	}

	manager, err := NewJWTManager(config)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	// Create mock user
	user := &AuthUser{
		ID:    123,
		Email: "test@example.com",
	}

	// Generate token
	token, err := manager.GenerateToken(user)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	if token == "" {
		t.Error("Token should not be empty")
	}

	// Validate token
	claims, err := manager.ValidateToken(token)
	if err != nil {
		t.Fatalf("Failed to validate token: %v", err)
	}

	// Check claims
	// UserID is stored as interface{}, so compare as such
	if fmt.Sprintf("%v", claims.UserID) != "123" {
		t.Errorf("UserID mismatch: got %v, want 123", claims.UserID)
	}

	// Test invalid token
	_, err = manager.ValidateToken("invalid-token")
	if err == nil {
		t.Error("Should error on invalid token")
	}

	// Test token with custom claims
	customClaims := map[string]interface{}{
		"email": "test@example.com",
		"role":  "admin",
	}

	tokenWithClaims, err := manager.GenerateToken(user, customClaims)
	if err != nil {
		t.Fatalf("Failed to generate token with claims: %v", err)
	}

	claimsWithCustom, err := manager.ValidateToken(tokenWithClaims)
	if err != nil {
		t.Fatalf("Failed to validate token with claims: %v", err)
	}

	if claimsWithCustom.Email != "test@example.com" {
		t.Errorf("Email claim mismatch: got %v, want test@example.com", claimsWithCustom.Email)
	}

	if claimsWithCustom.Role != "admin" {
		t.Errorf("Role claim mismatch: got %v, want admin", claimsWithCustom.Role)
	}
}

func TestJWTBlacklist(t *testing.T) {
	config := JWTConfig{
		Secret:           "test-secret-key-for-testing-must-be-32",
		Algorithm:        "HS256",
		TTL:              60,
		BlacklistEnabled: true,
		BlacklistStore:   NewInMemoryBlacklistStore(),
	}

	manager, err := NewJWTManager(config)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	user := &AuthUser{ID: 1}
	token, _ := manager.GenerateToken(user)

	// Token should be valid initially
	claims, err := manager.ValidateToken(token)
	if err != nil {
		t.Error("Token should be valid initially")
	}

	// Revoke token
	manager.RevokeToken(claims.ID)

	// Token should be invalid after revocation
	_, err = manager.ValidateToken(token)
	if err == nil {
		t.Error("Token should be invalid after revocation")
	}
}

func TestSessionManagement(t *testing.T) {
	session := NewSession("test-session-id")

	// Test Put and Get
	session.Put("user_id", 123)
	session.Put("username", "john")

	if session.Get("user_id") != 123 {
		t.Error("Should get correct user_id")
	}

	if session.Get("username") != "john" {
		t.Error("Should get correct username")
	}

	// Test Has
	if !session.Has("user_id") {
		t.Error("Should have user_id")
	}

	if session.Has("nonexistent") {
		t.Error("Should not have nonexistent key")
	}

	// Test Remove
	session.Remove("username")
	if session.Has("username") {
		t.Error("Should not have username after removal")
	}

	// Test Flash
	session.Flash("message", "Success!")

	// First get should return value
	if session.GetFlash("message") != "Success!" {
		t.Error("Should get flash message")
	}

	// Second get should return nil (flash consumed)
	if session.GetFlash("message") != nil {
		t.Error("Flash should be consumed after first get")
	}

	// Test Clear
	session.Clear()
	if session.Has("user_id") {
		t.Error("Should not have any data after clear")
	}

	// Test Regenerate
	oldID := session.ID()
	session.Regenerate()
	newID := session.ID()

	if oldID == newID {
		t.Error("Session ID should change after regenerate")
	}
}

func TestAuthManager(t *testing.T) {
	manager := NewManager()

	// Register a mock provider
	provider := NewORMUserProvider(nil, "User", nil)
	manager.RegisterProvider("users", provider)

	// Note: In a real test, we'd create actual guard instances
	// For now, we just test the manager structure

	// Test default guard
	manager.SetDefaultGuard("web")

	// Test provider retrieval
	retrievedProvider, err := manager.Provider("users")
	if err != nil {
		t.Errorf("Should find registered provider: %v", err)
	}

	if retrievedProvider == nil {
		t.Error("Provider should not be nil")
	}

	// Test non-existent provider
	_, err = manager.Provider("nonexistent")
	if err == nil {
		t.Error("Should error on non-existent provider")
	}
}

// mockGuard implements Guard for Manager tests.
type mockGuard struct {
	name     string
	user     Authenticatable
	checkVal bool
}

func (g *mockGuard) Check(*http.Request) bool                        { return g.checkVal }
func (g *mockGuard) User(*http.Request) Authenticatable              { return g.user }
func (g *mockGuard) ID(*http.Request) interface{}                    { return nil }
func (g *mockGuard) SetProvider(UserProvider)                        {}
func (g *mockGuard) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *mockGuard) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *mockGuard) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *mockGuard) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}

// mockProvider implements UserProvider for Manager tests.
type mockProvider struct{}

func (p *mockProvider) FindByID(interface{}) (Authenticatable, error) { return nil, nil }
func (p *mockProvider) FindByIDCtx(context.Context, interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockProvider) FindByCredentials(map[string]interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockProvider) FindByCredentialsCtx(context.Context, map[string]interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockProvider) ValidateCredentials(Authenticatable, map[string]interface{}) bool {
	return false
}
func (p *mockProvider) UpdateRememberToken(Authenticatable, string) error { return nil }
func (p *mockProvider) UpdateRememberTokenCtx(context.Context, Authenticatable, string) error {
	return nil
}

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager() returned nil")
	}
	if m.guards == nil {
		t.Error("guards map not initialized")
	}
	if m.providers == nil {
		t.Error("providers map not initialized")
	}
	if m.defaultGuard != "web" {
		t.Errorf("defaultGuard = %q, want %q", m.defaultGuard, "web")
	}
}

func TestManagerRegisterGuard(t *testing.T) {
	m := NewManager()
	g := &mockGuard{name: "session"}

	m.RegisterGuard("web", g)

	got, err := m.Guard("web")
	if err != nil {
		t.Fatalf("Guard() error: %v", err)
	}
	if got != g {
		t.Error("Guard() returned wrong instance")
	}
}

func TestManagerRegisterProvider(t *testing.T) {
	m := NewManager()
	p := &mockProvider{}

	m.RegisterProvider("users", p)

	got, err := m.Provider("users")
	if err != nil {
		t.Fatalf("Provider() error: %v", err)
	}
	if got != p {
		t.Error("Provider() returned wrong instance")
	}
}

func TestManagerGuard(t *testing.T) {
	m := NewManager()
	g := &mockGuard{name: "web"}
	m.RegisterGuard("web", g)

	tests := []struct {
		name    string
		guard   string
		wantErr bool
	}{
		{"nonexistent guard returns error", "api", true},
		{"empty name uses default guard", "", false},
		{"named guard returns guard", "web", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.Guard(tt.guard)
			if (err != nil) != tt.wantErr {
				t.Errorf("Guard(%q) error = %v, wantErr %v", tt.guard, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got == nil {
				t.Error("Guard() returned nil")
			}
		})
	}
}

func TestManagerDefaultGuard(t *testing.T) {
	m := NewManager()

	// No guard registered — should error
	_, err := m.DefaultGuard()
	if err == nil {
		t.Error("DefaultGuard() should error when no guard is registered")
	}

	// Register and retrieve
	g := &mockGuard{name: "web"}
	m.RegisterGuard("web", g)
	got, err := m.DefaultGuard()
	if err != nil {
		t.Fatalf("DefaultGuard() error: %v", err)
	}
	if got != g {
		t.Error("DefaultGuard() returned wrong instance")
	}

	// Custom default
	g2 := &mockGuard{name: "api"}
	m.RegisterGuard("api", g2)
	m.SetDefaultGuard("api")
	got, err = m.DefaultGuard()
	if err != nil {
		t.Fatalf("DefaultGuard() after SetDefaultGuard error: %v", err)
	}
	if got != g2 {
		t.Error("DefaultGuard() did not switch to api guard")
	}
}

func TestManagerSetDefaultGuard(t *testing.T) {
	m := NewManager()

	m.SetDefaultGuard("jwt")
	if m.defaultGuard != "jwt" {
		t.Errorf("defaultGuard = %q, want %q", m.defaultGuard, "jwt")
	}

	m.SetDefaultGuard("session")
	if m.defaultGuard != "session" {
		t.Errorf("defaultGuard = %q, want %q", m.defaultGuard, "session")
	}
}

func TestManagerConcurrency(t *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("guard-%d", idx)

			// Register guard
			m.RegisterGuard(name, &mockGuard{name: name})

			// Register provider
			m.RegisterProvider(name, &mockProvider{})

			// Retrieve guard
			_, _ = m.Guard(name)

			// Retrieve provider
			_, _ = m.Provider(name)

			// Default guard operations
			m.SetDefaultGuard(name)
			_, _ = m.DefaultGuard()
		}(i)
	}
	wg.Wait()
}

func TestORMUserProvider(t *testing.T) {
	// Initialize SQLite in-memory database for testing
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Shutdown(context.Background())

	// Schema matches the columns ORMUserProvider expects to SELECT / UPDATE.
	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (name, email, password) VALUES ($1, $2, $3)", "Test User", "test@example.com", string(hashedPassword))
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)

	// Test FindByCredentials with valid email
	user, err := provider.FindByCredentials(map[string]interface{}{
		"email": "test@example.com",
	})

	if err != nil {
		t.Errorf("Should find user by email: %v", err)
	}

	if user == nil {
		t.Error("User should not be nil")
	}

	// Test FindByCredentials with invalid email
	_, err = provider.FindByCredentials(map[string]interface{}{
		"email": "nonexistent@example.com",
	})

	if err == nil {
		t.Error("Should error on non-existent user")
	}

	// Test ValidateCredentials with correct password
	user, err = provider.FindByCredentials(map[string]interface{}{
		"email": "test@example.com",
	})
	if err != nil {
		t.Fatalf("Should find user for validation: %v", err)
	}

	valid := provider.ValidateCredentials(user, map[string]interface{}{
		"password": "password", // The user has bcrypt hash for "password"
	})

	if !valid {
		t.Error("Should validate correct password")
	}

	// Test ValidateCredentials with wrong password
	valid = provider.ValidateCredentials(user, map[string]interface{}{
		"password": "wrong-password",
	})

	if valid {
		t.Error("Should not validate wrong password")
	}

	// Test FindByID
	userByID, err := provider.FindByID(1)
	if err != nil {
		t.Errorf("Should find user by ID: %v", err)
	}

	if userByID == nil {
		t.Error("User should not be nil")
	}

	// Test UpdateRememberToken
	token := "remember-token"
	err = provider.UpdateRememberToken(user, token)
	if err != nil {
		t.Errorf("Should update remember token: %v", err)
	}

	if user.GetRememberToken() != token {
		t.Error("Remember token should be updated")
	}
}

func TestORMUserProviderCompareAndSwapRememberToken(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}
	_, err = db.Exec("INSERT INTO users (name, email, password, remember_token) VALUES ($1, $2, $3, $4)", "Test User", "test@example.com", "hash", "old-hash")
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)

	// The optional capability must be implemented so SessionGuard's
	// recall path takes the atomic branch for the default provider.
	var _ RememberTokenCompareAndSwapper = provider

	user, err := provider.FindByID(1)
	if err != nil || user == nil {
		t.Fatalf("FindByID: user=%v err=%v", user, err)
	}

	// Matching old token: swap succeeds and persists.
	swapped, err := provider.CompareAndSwapRememberToken(context.Background(), user, "old-hash", "new-hash")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("swap with matching old token should succeed")
	}
	if user.GetRememberToken() != "new-hash" {
		t.Errorf("in-memory token = %q, want %q", user.GetRememberToken(), "new-hash")
	}
	reloaded, err := provider.FindByID(1)
	if err != nil || reloaded == nil {
		t.Fatalf("FindByID after swap: user=%v err=%v", reloaded, err)
	}
	if reloaded.GetRememberToken() != "new-hash" {
		t.Errorf("persisted token = %q, want %q", reloaded.GetRememberToken(), "new-hash")
	}

	// Stale old token (a concurrent rotation already replaced it): the
	// swap must report false, leave the row untouched, and not mutate
	// the in-memory user.
	swapped, err = provider.CompareAndSwapRememberToken(context.Background(), reloaded, "old-hash", "loser-hash")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken (stale): %v", err)
	}
	if swapped {
		t.Fatal("swap with stale old token must report false")
	}
	if reloaded.GetRememberToken() != "new-hash" {
		t.Errorf("in-memory token mutated on failed swap: %q", reloaded.GetRememberToken())
	}
	final, err := provider.FindByID(1)
	if err != nil || final == nil {
		t.Fatalf("FindByID after stale swap: user=%v err=%v", final, err)
	}
	if final.GetRememberToken() != "new-hash" {
		t.Errorf("persisted token after stale swap = %q, want %q", final.GetRememberToken(), "new-hash")
	}

	// Uninitialized DB errors instead of reporting a clean miss.
	nilProvider := NewORMUserProvider(nil, "User", nil)
	if _, err := nilProvider.CompareAndSwapRememberToken(context.Background(), user, "a", "b"); err == nil {
		t.Error("expected error for uninitialized database")
	}
}

// TestORMUserProviderPlaceholderDialect pins the placeholder selection:
// "postgres" emits $N, everything else emits ?. Pre-fix every provider
// statement hardcoded $N, which is a syntax error on MySQL; combined with
// the fail-closed rotate-on-use recall, every remember-cookie recall on
// MySQL was silently rejected.
func TestORMUserProviderPlaceholderDialect(t *testing.T) {
	for dialect, want1 := range map[string]string{
		"postgres": "$1",
		"mysql":    "?",
		"sqlite":   "?",
		"":         "?",
	} {
		p := NewORMUserProviderForDialect(nil, "User", nil, dialect)
		if got := p.ph(1); got != want1 {
			t.Errorf("dialect %q: ph(1) = %q, want %q", dialect, got, want1)
		}
	}
	if got := NewORMUserProviderForDialect(nil, "User", nil, "postgres").ph(3); got != "$3" {
		t.Errorf(`postgres ph(3) = %q, want "$3"`, got)
	}
	// The plain constructor keeps the historical PostgreSQL syntax.
	if got := NewORMUserProvider(nil, "User", nil).ph(2); got != "$2" {
		t.Errorf(`default ph(2) = %q, want "$2"`, got)
	}
}

// TestORMUserProviderQuestionMarkDialectEndToEnd runs every provider
// statement (FindByID, FindByCredentials, UpdateRememberToken,
// CompareAndSwapRememberToken) through a ?-placeholder provider against
// SQLite, which accepts ? but would also accept $N, so the point of this
// test is that the rewritten statements bind correctly with ? ordering.
func TestORMUserProviderQuestionMarkDialectEndToEnd(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	if _, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`); err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}
	if _, err = db.Exec("INSERT INTO users (name, email, password, remember_token) VALUES (?, ?, ?, ?)", "Test User", "test@example.com", "hash", "old-hash"); err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProviderForDialect(db, "User", nil, "sqlite")

	user, err := provider.FindByID(1)
	if err != nil || user == nil {
		t.Fatalf("FindByID: user=%v err=%v", user, err)
	}
	byEmail, err := provider.FindByCredentials(map[string]interface{}{"email": "test@example.com"})
	if err != nil || byEmail == nil {
		t.Fatalf("FindByCredentials: user=%v err=%v", byEmail, err)
	}
	if err := provider.UpdateRememberToken(user, "updated-hash"); err != nil {
		t.Fatalf("UpdateRememberToken: %v", err)
	}
	swapped, err := provider.CompareAndSwapRememberToken(context.Background(), user, "updated-hash", "swapped-hash")
	if err != nil {
		t.Fatalf("CompareAndSwapRememberToken: %v", err)
	}
	if !swapped {
		t.Fatal("swap with matching old token should succeed")
	}
	final, err := provider.FindByID(1)
	if err != nil || final == nil {
		t.Fatalf("FindByID after swap: user=%v err=%v", final, err)
	}
	if final.GetRememberToken() != "swapped-hash" {
		t.Errorf("persisted token = %q, want %q", final.GetRememberToken(), "swapped-hash")
	}
}

func TestManagerGate(t *testing.T) {
	m := NewManager()

	if m.Gate() == nil {
		t.Fatal("Gate() should not be nil on a new Manager")
	}

	// Define a gate and verify it works through the manager
	m.Gate().Define("edit-post", func(user Authenticatable, args ...interface{}) bool {
		if len(args) == 0 {
			return false
		}
		post, ok := args[0].(*mockPost)
		if !ok {
			return false
		}
		return user.GetAuthIdentifier() == post.AuthorID
	})

	user := &mockUser{id: 1}
	guard := &mockGuard{name: "web", user: user, checkVal: true}
	m.RegisterGuard("web", guard)

	r := httptest.NewRequest("GET", "/", nil)

	ownPost := &mockPost{ID: 1, AuthorID: 1}
	otherPost := &mockPost{ID: 2, AuthorID: 2}

	if !m.GateAllows(r, "edit-post", ownPost) {
		t.Error("expected GateAllows to return true for own post")
	}

	if m.GateAllows(r, "edit-post", otherPost) {
		t.Error("expected GateAllows to return false for other's post")
	}
}

func TestManagerGateAllows_NoUser(t *testing.T) {
	m := NewManager()

	// Guard that returns no user
	guard := &mockGuard{name: "web", user: nil, checkVal: false}
	m.RegisterGuard("web", guard)

	m.Gate().Define("anything", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	r := httptest.NewRequest("GET", "/", nil)

	if m.GateAllows(r, "anything") {
		t.Error("expected GateAllows to return false when no user is authenticated")
	}
}

func TestManagerGateAllows_NoGuard(t *testing.T) {
	m := NewManager()

	// No guard registered at all
	r := httptest.NewRequest("GET", "/", nil)

	if m.GateAllows(r, "anything") {
		t.Error("expected GateAllows to return false when no guard is registered")
	}
}

func TestManagerGateAuthorize(t *testing.T) {
	m := NewManager()

	user := &mockUser{id: 1}
	guard := &mockGuard{name: "web", user: user, checkVal: true}
	m.RegisterGuard("web", guard)

	m.Gate().Define("create-post", func(u Authenticatable, args ...interface{}) bool {
		return true
	})
	m.Gate().Define("delete-post", func(u Authenticatable, args ...interface{}) bool {
		return false
	})

	r := httptest.NewRequest("GET", "/", nil)

	// Allowed ability should return nil
	if err := m.GateAuthorize(r, "create-post"); err != nil {
		t.Errorf("expected nil error for allowed ability, got %v", err)
	}

	// Denied ability should return ErrUnauthorized
	if err := m.GateAuthorize(r, "delete-post"); err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestManagerGateAuthorize_NoUser(t *testing.T) {
	m := NewManager()

	guard := &mockGuard{name: "web", user: nil, checkVal: false}
	m.RegisterGuard("web", guard)

	r := httptest.NewRequest("GET", "/", nil)

	if err := m.GateAuthorize(r, "anything"); err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized when no user, got %v", err)
	}
}

func BenchmarkPasswordHashing(b *testing.B) {
	hasher := NewBcryptHasher(10)
	password := "benchmark-password"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = hasher.Hash(password)
	}
}

func BenchmarkPasswordVerification(b *testing.B) {
	hasher := NewBcryptHasher(10)
	password := "benchmark-password"
	hash, _ := hasher.Hash(password)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = hasher.Verify(password, hash)
	}
}

func BenchmarkJWTGeneration(b *testing.B) {
	config := JWTConfig{
		Secret:    "benchmark-secret-must-be-at-least-32-b",
		Algorithm: "HS256",
		TTL:       60,
	}
	manager, err := NewJWTManager(config)
	if err != nil {
		b.Fatalf("NewJWTManager: %v", err)
	}
	user := &AuthUser{ID: 1}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = manager.GenerateToken(user)
	}
}

func BenchmarkJWTValidation(b *testing.B) {
	config := JWTConfig{
		Secret:    "benchmark-secret-must-be-at-least-32-b",
		Algorithm: "HS256",
		TTL:       60,
	}
	manager, err := NewJWTManager(config)
	if err != nil {
		b.Fatalf("NewJWTManager: %v", err)
	}
	user := &AuthUser{ID: 1}
	token, _ := manager.GenerateToken(user)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = manager.ValidateToken(token)
	}
}

// Integration test for session-based auth flow: provider lookup, password
// verification, session put/has, and invalidate.
func TestSessionAuthFlow(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (name, email, password) VALUES ($1, $2, $3)", "Test User", "test@example.com", string(hashedPassword))
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)

	credentials := map[string]interface{}{
		"email":    "test@example.com",
		"password": "password",
	}

	user, err := provider.FindByCredentials(credentials)
	if err != nil {
		t.Fatalf("Failed to find user: %v", err)
	}

	if !provider.ValidateCredentials(user, credentials) {
		t.Fatal("ValidateCredentials should succeed with correct password")
	}

	wrongCreds := map[string]interface{}{"password": "wrong-password"}
	if provider.ValidateCredentials(user, wrongCreds) {
		t.Error("ValidateCredentials should fail with wrong password")
	}

	session := NewSession("")
	session.Put("user_id", user.GetAuthIdentifier())

	if !session.Has("user_id") {
		t.Error("User ID should be in session")
	}

	session.Invalidate()

	if session.Has("user_id") {
		t.Error("User ID should not be in session after logout")
	}
}

// Integration test for JWT auth flow: provider lookup, password verification,
// token generation, and Bearer header round-trip with claims verification.
func TestJWTAuthFlow(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL DEFAULT '',
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL,
			remember_token TEXT
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (name, email, password) VALUES ($1, $2, $3)", "Test User", "test@example.com", string(hashedPassword))
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)
	config := JWTConfig{
		Secret:    "test-secret-key-for-testing-must-be-32",
		Algorithm: "HS256",
		TTL:       60,
	}
	jwtMgr, err := NewJWTManager(config)
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	credentials := map[string]interface{}{
		"email":    "test@example.com",
		"password": "password",
	}

	user, err := provider.FindByCredentials(credentials)
	if err != nil {
		t.Fatalf("FindByCredentials: %v", err)
	}
	if !provider.ValidateCredentials(user, credentials) {
		t.Fatal("ValidateCredentials should succeed with correct password")
	}

	token, err := jwtMgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	authHeader := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		t.Fatalf("expected Bearer header, got %q", authHeader)
	}
	tokenFromHeader := strings.TrimPrefix(authHeader, "Bearer ")

	claims, err := jwtMgr.ValidateToken(tokenFromHeader)
	if err != nil {
		t.Fatalf("Failed to validate token from header: %v", err)
	}

	if fmt.Sprintf("%v", claims.UserID) != fmt.Sprintf("%v", user.GetAuthIdentifier()) {
		t.Errorf("User ID mismatch: claims=%v user=%v", claims.UserID, user.GetAuthIdentifier())
	}
}
