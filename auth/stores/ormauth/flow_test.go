package ormauth_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// TestSessionAuthFlow is the integration test for session-based auth:
// user store lookup, password verification, session put/has, and invalidate.
func TestSessionAuthFlow(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	userStore := newStore(t)

	credentials := map[string]interface{}{
		"email":    testEmail,
		"password": testPassword,
	}

	user, err := userStore.FindByCredentials(credentials)
	if err != nil {
		t.Fatalf("FindByCredentials: %v", err)
	}
	if !userStore.ValidateCredentials(user, credentials) {
		t.Fatal("ValidateCredentials should succeed with the correct password")
	}
	if userStore.ValidateCredentials(user, map[string]interface{}{"password": "wrong-password"}) {
		t.Error("ValidateCredentials should fail with the wrong password")
	}

	session := auth.NewSession("")
	session.Put("user_id", user.GetAuthIdentifier())
	if !session.Has("user_id") {
		t.Error("user ID should be in the session")
	}

	session.Invalidate()
	if session.Has("user_id") {
		t.Error("user ID should be gone after invalidate")
	}
}

// TestJWTAuthFlow is the integration test for JWT auth: user store lookup,
// password verification, token generation, and a Bearer header round-trip
// with claims verification.
func TestJWTAuthFlow(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	userStore := newStore(t)

	jwtMgr, err := auth.NewJWTManager(auth.JWTConfig{
		Secret:    "test-secret-key-for-testing-must-be-32",
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	credentials := map[string]interface{}{
		"email":    testEmail,
		"password": testPassword,
	}

	user, err := userStore.FindByCredentials(credentials)
	if err != nil {
		t.Fatalf("FindByCredentials: %v", err)
	}
	if !userStore.ValidateCredentials(user, credentials) {
		t.Fatal("ValidateCredentials should succeed with the correct password")
	}

	token, err := jwtMgr.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	authHeader := req.Header.Get("Authorization")
	if !strings.HasPrefix(authHeader, "Bearer ") {
		t.Fatalf("expected Bearer header, got %q", authHeader)
	}

	claims, err := jwtMgr.ValidateToken(strings.TrimPrefix(authHeader, "Bearer "))
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if fmt.Sprintf("%v", claims.UserID) != fmt.Sprintf("%v", user.GetAuthIdentifier()) {
		t.Errorf("user ID mismatch: claims=%v user=%v", claims.UserID, user.GetAuthIdentifier())
	}
}
