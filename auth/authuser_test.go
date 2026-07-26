package auth

import (
	"testing"
)

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
		expected := "AuthUser<42>"
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
