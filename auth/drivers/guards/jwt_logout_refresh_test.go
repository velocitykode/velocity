package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// jwtLogoutRefreshUser is the minimal Authenticatable used by the
// JWT-logout refresh-generation suite.
type jwtLogoutRefreshUser struct {
	id string
}

func (u *jwtLogoutRefreshUser) GetAuthIdentifier() interface{} { return u.id }
func (u *jwtLogoutRefreshUser) GetAuthPassword() string        { return "" }
func (u *jwtLogoutRefreshUser) GetRememberToken() string       { return "" }
func (u *jwtLogoutRefreshUser) SetRememberToken(string)        {}

// jwtLogoutRefreshStore returns the user for any id so the
// JWTManager.RefreshToken path can resolve through it.
type jwtLogoutRefreshStore struct {
	user *jwtLogoutRefreshUser
}

func (p *jwtLogoutRefreshStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	return p.user, nil
}
func (p *jwtLogoutRefreshStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return p.user, nil
}
func (p *jwtLogoutRefreshStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *jwtLogoutRefreshStore) UpdateRememberToken(auth.Authenticatable, string) error {
	return nil
}

// TestJWTScheme_Logout_BumpsRefreshGeneration is the H-07 regression test.
// After Logout, a previously issued refresh token MUST fail with
// ErrRefreshGenerationStale even though it is still inside its TTL and its
// JTI is not on the blacklist.
func TestJWTScheme_Logout_BumpsRefreshGeneration(t *testing.T) {
	user := &jwtLogoutRefreshUser{id: "user-7"}
	userStore := &jwtLogoutRefreshStore{user: user}

	scheme, err := NewJWTScheme(userStore, auth.JWTConfig{
		Secret:     strings.Repeat("s", 64),
		Algorithm:  "HS256",
		TTL:        60,
		RefreshTTL: 20160,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}

	// Mint an access token (used to drive Logout) and a refresh token.
	accessToken, err := scheme.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	refreshToken, err := scheme.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	// Sanity: refresh works before Logout.
	if _, err := scheme.RefreshToken(refreshToken); err != nil {
		t.Fatalf("RefreshToken pre-logout returned %v; expected success", err)
	}

	// Logout: server-side state changes (access JTI blacklisted +
	// refresh generation bumped).
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.Header.Set("Authorization", "Bearer "+accessToken)
	if err := scheme.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// Refresh token MUST now be rejected as stale. Without the fix
	// the same call returns a fresh access token (the captured-
	// refresh-token attack).
	_, err = scheme.RefreshToken(refreshToken)
	if err == nil {
		t.Fatal("RefreshToken post-logout returned nil; expected ErrRefreshGenerationStale")
	}
	if !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Fatalf("expected ErrRefreshGenerationStale, got %v", err)
	}
}

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *jwtLogoutRefreshStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *jwtLogoutRefreshStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *jwtLogoutRefreshStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
