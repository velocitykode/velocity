package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
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

	// Register a mock user store
	userStore := &mockStore{}
	manager.RegisterUserStore("users", userStore)

	// Note: In a real test, we'd create actual scheme instances
	// For now, we just test the manager structure

	// Test default scheme
	manager.SetDefaultScheme("web")

	// Test user store retrieval
	retrievedStore, err := manager.UserStore("users")
	if err != nil {
		t.Errorf("Should find registered user store: %v", err)
	}

	if retrievedStore == nil {
		t.Error("Store should not be nil")
	}

	// Test non-existent user store
	_, err = manager.UserStore("nonexistent")
	if err == nil {
		t.Error("Should error on non-existent provider")
	}
}

// mockScheme implements Scheme for Manager tests.
type mockScheme struct {
	name     string
	user     Authenticatable
	checkVal bool
}

func (g *mockScheme) Check(*http.Request) bool                        { return g.checkVal }
func (g *mockScheme) User(*http.Request) Authenticatable              { return g.user }
func (g *mockScheme) ID(*http.Request) interface{}                    { return nil }
func (g *mockScheme) SetUserStore(UserStore)                          {}
func (g *mockScheme) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *mockScheme) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *mockScheme) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *mockScheme) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}

// mockStore implements UserStore for Manager tests.
type mockStore struct{}

func (p *mockStore) FindByID(interface{}) (Authenticatable, error) { return nil, nil }
func (p *mockStore) FindByIDCtx(context.Context, interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockStore) FindByCredentials(map[string]interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockStore) ValidateCredentials(Authenticatable, map[string]interface{}) bool {
	return false
}
func (p *mockStore) UpdateRememberToken(Authenticatable, string) error { return nil }
func (p *mockStore) UpdateRememberTokenCtx(context.Context, Authenticatable, string) error {
	return nil
}

func TestNewManager(t *testing.T) {
	m := NewManager()
	if m == nil {
		t.Fatal("NewManager() returned nil")
	}
	if m.schemes == nil {
		t.Error("schemes map not initialized")
	}
	if m.userStores == nil {
		t.Error("providers map not initialized")
	}
	if m.defaultScheme != "web" {
		t.Errorf("defaultScheme = %q, want %q", m.defaultScheme, "web")
	}
}

func TestManagerRegisterScheme(t *testing.T) {
	m := NewManager()
	g := &mockScheme{name: "session"}

	m.RegisterScheme("web", g)

	got, err := m.Scheme("web")
	if err != nil {
		t.Fatalf("Scheme() error: %v", err)
	}
	if got != g {
		t.Error("Scheme() returned wrong instance")
	}
}

func TestManagerRegisterUserStore(t *testing.T) {
	m := NewManager()
	p := &mockStore{}

	m.RegisterUserStore("users", p)

	got, err := m.UserStore("users")
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}
	if got != p {
		t.Error("Store() returned wrong instance")
	}
}

func TestManagerScheme(t *testing.T) {
	m := NewManager()
	g := &mockScheme{name: "web"}
	m.RegisterScheme("web", g)

	tests := []struct {
		name    string
		scheme  string
		wantErr bool
	}{
		{"nonexistent scheme returns error", "api", true},
		{"empty name uses default scheme", "", false},
		{"named scheme returns scheme", "web", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.Scheme(tt.scheme)
			if (err != nil) != tt.wantErr {
				t.Errorf("Scheme(%q) error = %v, wantErr %v", tt.scheme, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got == nil {
				t.Error("Scheme() returned nil")
			}
		})
	}
}

func TestManagerDefaultScheme(t *testing.T) {
	m := NewManager()

	// No scheme registered — should error
	_, err := m.DefaultScheme()
	if err == nil {
		t.Error("DefaultScheme() should error when no scheme is registered")
	}

	// Register and retrieve
	g := &mockScheme{name: "web"}
	m.RegisterScheme("web", g)
	got, err := m.DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme() error: %v", err)
	}
	if got != g {
		t.Error("DefaultScheme() returned wrong instance")
	}

	// Custom default
	g2 := &mockScheme{name: "api"}
	m.RegisterScheme("api", g2)
	m.SetDefaultScheme("api")
	got, err = m.DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme() after SetDefaultScheme error: %v", err)
	}
	if got != g2 {
		t.Error("DefaultScheme() did not switch to api scheme")
	}
}

func TestManagerSetDefaultScheme(t *testing.T) {
	m := NewManager()

	m.SetDefaultScheme("jwt")
	if m.defaultScheme != "jwt" {
		t.Errorf("defaultScheme = %q, want %q", m.defaultScheme, "jwt")
	}

	m.SetDefaultScheme("session")
	if m.defaultScheme != "session" {
		t.Errorf("defaultScheme = %q, want %q", m.defaultScheme, "session")
	}
}

func TestManagerConcurrency(t *testing.T) {
	m := NewManager()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("scheme-%d", idx)

			// Register scheme
			m.RegisterScheme(name, &mockScheme{name: name})

			// Register user store
			m.RegisterUserStore(name, &mockStore{})

			// Retrieve scheme
			_, _ = m.Scheme(name)

			// Retrieve user store
			_, _ = m.UserStore(name)

			// Default scheme operations
			m.SetDefaultScheme(name)
			_, _ = m.DefaultScheme()
		}(i)
	}
	wg.Wait()
}

func TestManagerAccess(t *testing.T) {
	m := NewManager()

	if m.Access() == nil {
		t.Fatal("Access() should not be nil on a new Manager")
	}

	// Define an access rule and verify it works through the manager
	m.Access().Define("edit-post", func(user Authenticatable, args ...interface{}) bool {
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
	scheme := &mockScheme{name: "web", user: user, checkVal: true}
	m.RegisterScheme("web", scheme)

	r := httptest.NewRequest("GET", "/", nil)

	ownPost := &mockPost{ID: 1, AuthorID: 1}
	otherPost := &mockPost{ID: 2, AuthorID: 2}

	if !m.Allows(r, "edit-post", ownPost) {
		t.Error("expected Allows to return true for own post")
	}

	if m.Allows(r, "edit-post", otherPost) {
		t.Error("expected Allows to return false for other's post")
	}
}

func TestManagerAllows_NoUser(t *testing.T) {
	m := NewManager()

	// Scheme that returns no user
	scheme := &mockScheme{name: "web", user: nil, checkVal: false}
	m.RegisterScheme("web", scheme)

	m.Access().Define("anything", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	r := httptest.NewRequest("GET", "/", nil)

	if m.Allows(r, "anything") {
		t.Error("expected Allows to return false when no user is authenticated")
	}
}

func TestManagerAllows_NoScheme(t *testing.T) {
	m := NewManager()

	// No scheme registered at all
	r := httptest.NewRequest("GET", "/", nil)

	if m.Allows(r, "anything") {
		t.Error("expected Allows to return false when no scheme is registered")
	}
}

func TestManagerAuthorize(t *testing.T) {
	m := NewManager()

	user := &mockUser{id: 1}
	scheme := &mockScheme{name: "web", user: user, checkVal: true}
	m.RegisterScheme("web", scheme)

	m.Access().Define("create-post", func(u Authenticatable, args ...interface{}) bool {
		return true
	})
	m.Access().Define("delete-post", func(u Authenticatable, args ...interface{}) bool {
		return false
	})

	r := httptest.NewRequest("GET", "/", nil)

	// Allowed ability should return nil
	if err := m.Authorize(r, "create-post"); err != nil {
		t.Errorf("expected nil error for allowed ability, got %v", err)
	}

	// Denied ability should return ErrUnauthorized
	if err := m.Authorize(r, "delete-post"); err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestManagerAuthorize_NoUser(t *testing.T) {
	m := NewManager()

	scheme := &mockScheme{name: "web", user: nil, checkVal: false}
	m.RegisterScheme("web", scheme)

	r := httptest.NewRequest("GET", "/", nil)

	if err := m.Authorize(r, "anything"); err != ErrUnauthorized {
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
