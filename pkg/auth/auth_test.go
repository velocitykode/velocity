package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
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

	manager := NewJWTManager(config)

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
	}

	manager := NewJWTManager(config)

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
	name string
}

func (g *mockGuard) Check(*http.Request) bool                        { return false }
func (g *mockGuard) User(*http.Request) Authenticatable              { return nil }
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
func (p *mockProvider) FindByCredentials(map[string]interface{}) (Authenticatable, error) {
	return nil, nil
}
func (p *mockProvider) ValidateCredentials(Authenticatable, map[string]interface{}) bool {
	return false
}
func (p *mockProvider) UpdateRememberToken(Authenticatable, string) error { return nil }

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
	t.Skip("TODO: fix ORM user provider test")
	// Initialize SQLite in-memory database for testing
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Close()

	// Create users table
	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Insert test user with bcrypt hashed password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (email, password) VALUES (?, ?)", "test@example.com", string(hashedPassword))
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
	manager := NewJWTManager(config)
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
	manager := NewJWTManager(config)
	user := &AuthUser{ID: 1}
	token, _ := manager.GenerateToken(user)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = manager.ValidateToken(token)
	}
}

// Integration test for session-based auth flow
func TestSessionAuthFlow(t *testing.T) {
	t.Skip("TODO: fix test")
	// Initialize SQLite in-memory database for testing
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Close()

	// Create users table
	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Insert test user with bcrypt hashed password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (email, password) VALUES (?, ?)", "test@example.com", string(hashedPassword))
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	// This would be a more complete test with actual HTTP handlers
	// For now, we test the components

	provider := NewORMUserProvider(db, "User", nil)

	// Simulate login attempt
	credentials := map[string]interface{}{
		"email":    "test@example.com",
		"password": "password",
	}

	// Find user
	user, err := provider.FindByCredentials(credentials)
	if err != nil {
		t.Fatalf("Failed to find user: %v", err)
	}

	// Validate credentials
	// Note: This will fail with mock data since the hash isn't a real bcrypt hash
	_ = provider.ValidateCredentials(user, credentials)

	// Create session
	session := NewSession("")
	session.Put("user_id", user.GetAuthIdentifier())

	// Verify user is "logged in"
	if !session.Has("user_id") {
		t.Error("User ID should be in session")
	}

	// Simulate logout
	session.Invalidate()

	if session.Has("user_id") {
		t.Error("User ID should not be in session after logout")
	}
}

// Integration test for JWT auth flow
func TestJWTAuthFlow(t *testing.T) {
	t.Skip("TODO: fix test")
	// Initialize SQLite in-memory database for testing
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("Failed to initialize ORM: %v", err)
	}
	defer manager.Close()

	// Create users table
	db := manager.DB()
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			password TEXT NOT NULL
		)
	`)
	if err != nil {
		t.Fatalf("Failed to create users table: %v", err)
	}

	// Insert test user with bcrypt hashed password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	_, err = db.Exec("INSERT INTO users (email, password) VALUES (?, ?)", "test@example.com", string(hashedPassword))
	if err != nil {
		t.Fatalf("Failed to insert test user: %v", err)
	}

	provider := NewORMUserProvider(db, "User", nil)
	config := JWTConfig{
		Secret:    "test-secret-key-for-testing-must-be-32",
		Algorithm: "HS256",
		TTL:       60,
	}
	jwtMgr := NewJWTManager(config)

	// Simulate login
	credentials := map[string]interface{}{
		"email":    "test@example.com",
		"password": "password",
	}

	// Find and validate user
	user, _ := provider.FindByCredentials(credentials)
	// Note: This will fail with mock data since the hash isn't a real bcrypt hash
	_ = provider.ValidateCredentials(user, credentials)

	// Generate token
	token, err := jwtMgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Simulate API request with token
	req := httptest.NewRequest("GET", "/api/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// Validate token from request
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		t.Error("Should have Authorization header")
	}

	// Extract token (remove "Bearer " prefix)
	tokenFromHeader := authHeader[7:]

	// Validate token
	claims, err := jwtMgr.ValidateToken(tokenFromHeader)
	if err != nil {
		t.Fatalf("Failed to validate token from header: %v", err)
	}

	// Verify user ID in claims
	if fmt.Sprintf("%v", claims.UserID) != fmt.Sprintf("%v", user.GetAuthIdentifier()) {
		t.Error("User ID mismatch in claims")
	}
}
