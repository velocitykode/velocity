package auth

import (
	"errors"
	"strings"
	"testing"
)

type errRefreshGenerationStore struct {
	err error
}

func (s errRefreshGenerationStore) Current(string) (int64, error) {
	return 0, s.err
}

func (s errRefreshGenerationStore) Bump(string) (int64, error) {
	return 0, nil
}

func TestJWT_RefreshToken_FailsClosedOnRefreshGenerationStoreError(t *testing.T) {
	storeErr := errors.New("transport down")
	mgr, err := NewJWTManager(JWTConfig{
		Secret:                 strings.Repeat("s", 64),
		Algorithm:              "HS256",
		TTL:                    60,
		RefreshTTL:             20160,
		RefreshGenerationStore: errRefreshGenerationStore{err: storeErr},
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}

	user := &jwtRefreshTestUser{id: "user-store-down"}
	provider := &jwtRefreshTestProvider{user: user}

	refreshToken, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	token, err := mgr.RefreshToken(refreshToken, provider)
	if err == nil {
		t.Fatal("RefreshToken returned nil error; expected store transport error")
	}
	if errors.Is(err, storeErr) {
		t.Fatalf("RefreshToken exposed store error %v", err)
	}
	if got, want := err.Error(), "velocity/auth: refresh generation store unavailable"; got != want {
		t.Fatalf("RefreshToken error = %q; want %q", got, want)
	}
	if token != "" {
		t.Fatalf("RefreshToken token = %q; want empty token", token)
	}
	if provider.findByIDCalls != 0 {
		t.Fatalf("FindByID called %d times; want 0", provider.findByIDCalls)
	}
}

func TestJWT_RefreshToken_DefaultInMemoryStoreStillRefreshes(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	user := &jwtRefreshTestUser{id: "user-default-store"}
	provider := &jwtRefreshTestProvider{user: user}

	refreshToken, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	token, err := mgr.RefreshToken(refreshToken, provider)
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if token == "" {
		t.Fatal("RefreshToken returned empty token; expected access token")
	}
}
