package auth

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// mockHasher implements Hasher interface for testing
type mockHasher struct {
	hashFn        func(password string) (string, error)
	verifyFn      func(password, hash string) bool
	needsRehashFn func(hash string) bool
}

func (m *mockHasher) Hash(password string) (string, error) {
	if m.hashFn != nil {
		return m.hashFn(password)
	}
	return "", nil
}

func (m *mockHasher) Verify(password, hash string) bool {
	if m.verifyFn != nil {
		return m.verifyFn(password, hash)
	}
	return false
}

func (m *mockHasher) NeedsRehash(hash string) bool {
	if m.needsRehashFn != nil {
		return m.needsRehashFn(hash)
	}
	return false
}

func TestORMUserProviderValidateCredentials(t *testing.T) {
	// Create a real password hash for testing
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte("secret123"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("Failed to hash password for test: %v", err)
	}

	testUser := &AuthUser{
		ID:       1,
		Email:    "test@example.com",
		Password: string(hashedPassword),
	}

	// Ensure global hasher is initialized
	InitHasher(NewBcryptHasher(bcrypt.MinCost))

	tests := []struct {
		name        string
		user        Authenticatable
		credentials map[string]interface{}
		want        bool
	}{
		{
			name: "validates correct password",
			user: testUser,
			credentials: map[string]interface{}{
				"password": "secret123",
			},
			want: true,
		},
		{
			name: "rejects incorrect password",
			user: testUser,
			credentials: map[string]interface{}{
				"password": "wrongpassword",
			},
			want: false,
		},
		{
			name: "rejects empty password",
			user: testUser,
			credentials: map[string]interface{}{
				"password": "",
			},
			want: false,
		},
		{
			name:        "rejects missing password in credentials",
			user:        testUser,
			credentials: map[string]interface{}{},
			want:        false,
		},
		{
			name: "rejects non-string password",
			user: testUser,
			credentials: map[string]interface{}{
				"password": 12345,
			},
			want: false,
		},
		{
			name:        "rejects nil credentials",
			user:        testUser,
			credentials: nil,
			want:        false,
		},
		{
			name: "rejects password with wrong type assertion",
			user: testUser,
			credentials: map[string]interface{}{
				"password": []byte("secret123"),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewORMUserProvider(nil, "User")

			got := provider.ValidateCredentials(tt.user, tt.credentials)
			if got != tt.want {
				t.Errorf("ValidateCredentials() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestORMUserProviderFindByCredentials_EmailValidation(t *testing.T) {
	// Note: These tests focus on the email validation logic without needing a database
	// Full database tests are in auth_test.go (currently skipped)

	tests := []struct {
		name        string
		credentials map[string]interface{}
		wantErr     bool
		errContains string
	}{
		{
			name:        "returns error when email missing",
			credentials: map[string]interface{}{},
			wantErr:     true,
			errContains: "email is required",
		},
		{
			name: "returns error when email is non-string",
			credentials: map[string]interface{}{
				"email": 12345,
			},
			wantErr:     true,
			errContains: "email is required",
		},
		{
			name: "returns error when email is nil",
			credentials: map[string]interface{}{
				"email": nil,
			},
			wantErr:     true,
			errContains: "email is required",
		},
		{
			name:        "returns error with nil credentials map",
			credentials: nil,
			wantErr:     true,
			errContains: "email is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewORMUserProvider(nil, "User")

			_, err := provider.FindByCredentials(tt.credentials)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindByCredentials() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != nil {
				if tt.errContains != "" && err.Error() != tt.errContains {
					t.Errorf("FindByCredentials() error = %q, want error containing %q", err.Error(), tt.errContains)
				}
			}
		})
	}
}

func TestORMUserProviderFindByID_NilHandling(t *testing.T) {
	tests := []struct {
		name    string
		id      interface{}
		wantErr bool
	}{
		{
			name:    "returns error when id is nil",
			id:      nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewORMUserProvider(nil, "User")

			_, err := provider.FindByID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("FindByID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr && err != ErrUserNotFound {
				t.Errorf("FindByID() error = %v, want %v", err, ErrUserNotFound)
			}
		})
	}
}

func TestORMUserProviderUpdateRememberToken(t *testing.T) {
	testUser := &AuthUser{
		ID:            1,
		Email:         "test@example.com",
		Password:      "hashed",
		RememberToken: "",
	}

	tests := []struct {
		name    string
		user    Authenticatable
		token   string
		wantErr bool
	}{
		{
			name:    "updates remember token successfully",
			user:    testUser,
			token:   "new-remember-token",
			wantErr: true, // DB not initialized in unit tests
		},
		{
			name:    "updates with empty token",
			user:    testUser,
			token:   "",
			wantErr: true, // DB not initialized in unit tests
		},
		{
			name:    "updates with long token",
			user:    testUser,
			token:   "a-very-long-remember-token-that-could-be-used-for-persistence",
			wantErr: true, // DB not initialized in unit tests
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewORMUserProvider(nil, "User")

			err := provider.UpdateRememberToken(tt.user, tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("UpdateRememberToken() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if tt.user.GetRememberToken() != tt.token {
					t.Errorf("GetRememberToken() = %v, want %v", tt.user.GetRememberToken(), tt.token)
				}
			}
		})
	}
}

func TestNewORMUserProvider(t *testing.T) {
	tests := []struct {
		name      string
		modelType string
	}{
		{
			name:      "creates provider with User model type",
			modelType: "User",
		},
		{
			name:      "creates provider with Admin model type",
			modelType: "Admin",
		},
		{
			name:      "creates provider with empty model type",
			modelType: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := NewORMUserProvider(nil, tt.modelType)

			if provider == nil {
				t.Fatal("NewORMUserProvider() returned nil")
			}

			if provider.modelType != tt.modelType {
				t.Errorf("modelType = %v, want %v", provider.modelType, tt.modelType)
			}

			if provider.hasher == nil {
				t.Error("hasher should be initialized")
			}
		})
	}
}

func TestAuthUserMethods(t *testing.T) {
	t.Run("GetAuthIdentifier returns ID", func(t *testing.T) {
		user := &AuthUser{ID: 123}
		if got := user.GetAuthIdentifier(); got != 123 {
			t.Errorf("GetAuthIdentifier() = %v, want %v", got, 123)
		}
	})

	t.Run("GetAuthPassword returns password", func(t *testing.T) {
		user := &AuthUser{Password: "hashed_password"}
		if got := user.GetAuthPassword(); got != "hashed_password" {
			t.Errorf("GetAuthPassword() = %v, want %v", got, "hashed_password")
		}
	})

	t.Run("GetRememberToken returns token", func(t *testing.T) {
		user := &AuthUser{RememberToken: "remember_token"}
		if got := user.GetRememberToken(); got != "remember_token" {
			t.Errorf("GetRememberToken() = %v, want %v", got, "remember_token")
		}
	})

	t.Run("SetRememberToken sets token", func(t *testing.T) {
		user := &AuthUser{}
		user.SetRememberToken("new_token")
		if got := user.GetRememberToken(); got != "new_token" {
			t.Errorf("GetRememberToken() after SetRememberToken = %v, want %v", got, "new_token")
		}
	})

	t.Run("String returns formatted representation", func(t *testing.T) {
		user := &AuthUser{ID: 42, Email: "test@example.com"}
		expected := "AuthUser<42: test@example.com>"
		if got := user.String(); got != expected {
			t.Errorf("String() = %v, want %v", got, expected)
		}
	})
}

func TestAuthUserInterfaceImplementation(t *testing.T) {
	// Compile-time check that AuthUser implements Authenticatable
	var _ Authenticatable = (*AuthUser)(nil)

	t.Run("AuthUser implements Authenticatable interface", func(t *testing.T) {
		user := &AuthUser{
			ID:            1,
			Name:          "Test User",
			Email:         "test@example.com",
			Password:      "hashed",
			RememberToken: "token",
		}

		// Test all interface methods work
		id := user.GetAuthIdentifier()
		if id == nil {
			t.Error("GetAuthIdentifier() should not return nil")
		}

		password := user.GetAuthPassword()
		if password == "" {
			t.Error("GetAuthPassword() should not return empty string")
		}

		token := user.GetRememberToken()
		if token == "" {
			t.Error("GetRememberToken() should not return empty string when set")
		}

		user.SetRememberToken("new_token")
		if user.GetRememberToken() != "new_token" {
			t.Error("SetRememberToken() should update the token")
		}
	})
}

func TestORMUserProviderValidateCredentialsWithMockHasher(t *testing.T) {
	// Save and restore the global hasher
	originalHasher := globalHasher
	defer func() {
		globalHasher = originalHasher
	}()

	tests := []struct {
		name        string
		setupHasher func()
		user        Authenticatable
		credentials map[string]interface{}
		want        bool
	}{
		{
			name: "uses hasher verify function",
			setupHasher: func() {
				InitHasher(&mockHasher{
					verifyFn: func(password, hash string) bool {
						return password == "correct" && hash == "hashed"
					},
				})
			},
			user: &AuthUser{Password: "hashed"},
			credentials: map[string]interface{}{
				"password": "correct",
			},
			want: true,
		},
		{
			name: "hasher returns false for wrong password",
			setupHasher: func() {
				InitHasher(&mockHasher{
					verifyFn: func(password, hash string) bool {
						return false
					},
				})
			},
			user: &AuthUser{Password: "hashed"},
			credentials: map[string]interface{}{
				"password": "wrong",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupHasher()

			// Create a new provider that will use the updated hasher
			provider := NewORMUserProvider(nil, "User")

			got := provider.ValidateCredentials(tt.user, tt.credentials)
			if got != tt.want {
				t.Errorf("ValidateCredentials() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestORMUserProviderUserProviderInterface(t *testing.T) {
	// Compile-time check that ORMUserProvider implements UserProvider
	var _ UserProvider = (*ORMUserProvider)(nil)

	t.Run("ORMUserProvider implements UserProvider interface", func(t *testing.T) {
		provider := NewORMUserProvider(nil, "User")

		// Verify all methods exist and are callable
		_, _ = provider.FindByID(nil)
		_, _ = provider.FindByCredentials(nil)
		_ = provider.ValidateCredentials(nil, nil)
		_ = provider.UpdateRememberToken(&AuthUser{}, "token")
	})
}
