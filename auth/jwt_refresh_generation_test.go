package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// jwtRefreshTestUser is the minimal Authenticatable used by the
// refresh-generation suite.
type jwtRefreshTestUser struct {
	id string
}

func (u *jwtRefreshTestUser) GetAuthIdentifier() interface{} { return u.id }
func (u *jwtRefreshTestUser) GetAuthPassword() string        { return "" }
func (u *jwtRefreshTestUser) GetRememberToken() string       { return "" }
func (u *jwtRefreshTestUser) SetRememberToken(string)        {}

// jwtRefreshTestProvider returns the user for any id so the
// RefreshToken path can resolve through it.
type jwtRefreshTestProvider struct {
	user          *jwtRefreshTestUser
	findByIDCalls int
}

func (p *jwtRefreshTestProvider) FindByID(id interface{}) (Authenticatable, error) {
	p.findByIDCalls++
	return p.user, nil
}
func (p *jwtRefreshTestProvider) FindByIDCtx(context.Context, interface{}) (Authenticatable, error) {
	return p.user, nil
}
func (p *jwtRefreshTestProvider) FindByCredentials(map[string]interface{}) (Authenticatable, error) {
	return p.user, nil
}
func (p *jwtRefreshTestProvider) FindByCredentialsCtx(context.Context, map[string]interface{}) (Authenticatable, error) {
	return p.user, nil
}
func (p *jwtRefreshTestProvider) ValidateCredentials(Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *jwtRefreshTestProvider) UpdateRememberToken(Authenticatable, string) error { return nil }
func (p *jwtRefreshTestProvider) UpdateRememberTokenCtx(context.Context, Authenticatable, string) error {
	return nil
}

type erroringRefreshGenerationStore struct {
	err error
}

func (s erroringRefreshGenerationStore) Current(string) (int64, error) {
	return 0, s.err
}

func (s erroringRefreshGenerationStore) Bump(string) (int64, error) {
	return 0, s.err
}

// newJWTManagerForRefresh constructs a JWTManager wired with the in-memory
// refresh-generation store the H-07 fix added by default.
func newJWTManagerForRefresh(t *testing.T) *JWTManager {
	t.Helper()
	mgr, err := NewJWTManager(JWTConfig{
		Secret:     strings.Repeat("s", 64),
		Algorithm:  "HS256",
		TTL:        60,
		RefreshTTL: 20160,
	})
	if err != nil {
		t.Fatalf("NewJWTManager: %v", err)
	}
	return mgr
}

// TestJWT_RefreshToken_StaleAfterBump is the H-07 regression test. A
// refresh token issued before BumpRefreshGeneration MUST be rejected with
// ErrRefreshGenerationStale after the bump fires; this is what makes
// JWTGuard.Logout effective against stolen refresh tokens.
func TestJWT_RefreshToken_StaleAfterBump(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	user := &jwtRefreshTestUser{id: "user-1"}
	provider := &jwtRefreshTestProvider{user: user}

	// Issue a refresh token before any bump (generation 0).
	rt, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	// Sanity: with no bump, the token refreshes successfully.
	if _, err := mgr.RefreshToken(rt, provider); err != nil {
		t.Fatalf("RefreshToken pre-bump returned %v; expected success", err)
	}

	// Logout-equivalent: bump the generation for this user.
	if _, err := mgr.BumpRefreshGeneration("user-1"); err != nil {
		t.Fatalf("BumpRefreshGeneration: %v", err)
	}

	// Same refresh token, post-bump: MUST be rejected as stale.
	_, err = mgr.RefreshToken(rt, provider)
	if err == nil {
		t.Fatal("RefreshToken post-bump returned nil; expected ErrRefreshGenerationStale")
	}
	if !errors.Is(err, ErrRefreshGenerationStale) {
		t.Fatalf("expected ErrRefreshGenerationStale, got %v", err)
	}
}

func TestJWT_RefreshToken_FailsClosedWhenGenerationStoreErrors(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	user := &jwtRefreshTestUser{id: "user-store-error"}
	provider := &jwtRefreshTestProvider{user: user}

	rt, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	storeErr := errors.New("refresh generation store transport error")
	mgr.SetRefreshGenerationStore(erroringRefreshGenerationStore{err: storeErr})

	token, err := mgr.RefreshToken(rt, provider)
	if err == nil {
		t.Fatal("RefreshToken returned nil error; expected store failure")
	}
	if token != "" {
		t.Fatalf("RefreshToken returned token %q; expected empty token", token)
	}
	if errors.Is(err, storeErr) {
		t.Fatalf("RefreshToken exposed store error %v", err)
	}
	if got, want := err.Error(), "velocity/auth: refresh generation store unavailable"; got != want {
		t.Fatalf("RefreshToken error = %q; want %q", got, want)
	}
	if provider.findByIDCalls != 0 {
		t.Fatalf("FindByID called %d times; want 0", provider.findByIDCalls)
	}
}

// TestJWT_RefreshToken_FreshAfterBumpStillWorks pins the happy path: after
// a bump, NEWLY issued refresh tokens carry the new generation and refresh
// successfully.
func TestJWT_RefreshToken_FreshAfterBumpStillWorks(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	user := &jwtRefreshTestUser{id: "user-2"}
	provider := &jwtRefreshTestProvider{user: user}

	if _, err := mgr.BumpRefreshGeneration("user-2"); err != nil {
		t.Fatalf("BumpRefreshGeneration: %v", err)
	}

	rt, err := mgr.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if _, err := mgr.RefreshToken(rt, provider); err != nil {
		t.Fatalf("RefreshToken on freshly-minted token returned %v; expected success", err)
	}
}

// TestJWT_RefreshToken_DifferentUserNotAffected confirms a Logout for one
// user does NOT invalidate refresh tokens for any other user.
func TestJWT_RefreshToken_DifferentUserNotAffected(t *testing.T) {
	mgr := newJWTManagerForRefresh(t)
	userA := &jwtRefreshTestUser{id: "user-a"}
	userB := &jwtRefreshTestUser{id: "user-b"}
	provider := &jwtRefreshTestProvider{user: userB}

	// userB's refresh token, issued before any bump.
	rtB, err := mgr.GenerateRefreshToken(userB)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	// Bump userA only.
	if _, err := mgr.BumpRefreshGeneration("user-a"); err != nil {
		t.Fatalf("BumpRefreshGeneration: %v", err)
	}

	// userB's token MUST still refresh.
	if _, err := mgr.RefreshToken(rtB, provider); err != nil {
		t.Fatalf("RefreshToken for unrelated user-b returned %v; expected success", err)
	}

	_ = userA
}

// TestInMemoryRefreshGenerationStore_BumpReturnsNewValue covers the
// store contract directly so a swapped implementation can be tested
// against the same invariant.
func TestInMemoryRefreshGenerationStore_BumpReturnsNewValue(t *testing.T) {
	store := NewInMemoryRefreshGenerationStore()

	got, err := store.Current("u1")
	if err != nil || got != 0 {
		t.Fatalf("Current pre-Bump = (%d, %v); want (0, nil)", got, err)
	}

	got, err = store.Bump("u1")
	if err != nil || got != 1 {
		t.Fatalf("Bump #1 = (%d, %v); want (1, nil)", got, err)
	}

	got, err = store.Bump("u1")
	if err != nil || got != 2 {
		t.Fatalf("Bump #2 = (%d, %v); want (2, nil)", got, err)
	}

	got, err = store.Current("u1")
	if err != nil || got != 2 {
		t.Fatalf("Current post-Bump = (%d, %v); want (2, nil)", got, err)
	}
}
