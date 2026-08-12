package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// sharedRefreshGenStore is the test surrogate for a Redis-backed
// RefreshGenerationStore: a single counter map shared between two
// JWTScheme instances that simulate two hosts in a fleet.
type sharedRefreshGenStore struct {
	mu     sync.RWMutex
	counts map[string]int64
	bumps  atomic.Int32
}

func newSharedRefreshGenStore() *sharedRefreshGenStore {
	return &sharedRefreshGenStore{counts: make(map[string]int64)}
}

func (s *sharedRefreshGenStore) Current(userID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.counts[userID], nil
}

func (s *sharedRefreshGenStore) Bump(userID string) (int64, error) {
	s.bumps.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[userID]++
	return s.counts[userID], nil
}

// jwtSharedStoreUser implements auth.Authenticatable for the shared-store
// suite.
type jwtSharedStoreUser struct {
	id string
}

func (u *jwtSharedStoreUser) GetAuthIdentifier() interface{} { return u.id }
func (u *jwtSharedStoreUser) GetAuthPassword() string        { return "" }
func (u *jwtSharedStoreUser) GetRememberToken() string       { return "" }
func (u *jwtSharedStoreUser) SetRememberToken(string)        {}

// jwtSharedStoreUserStore returns the user for any id.
type jwtSharedStoreUserStore struct {
	user *jwtSharedStoreUser
}

func (p *jwtSharedStoreUserStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	return p.user, nil
}
func (p *jwtSharedStoreUserStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return p.user, nil
}
func (p *jwtSharedStoreUserStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *jwtSharedStoreUserStore) UpdateRememberToken(auth.Authenticatable, string) error {
	return nil
}

// newFleetScheme builds a JWTScheme wired against a caller-supplied shared
// RefreshGenerationStore. Used to simulate two hosts in a fleet.
func newFleetScheme(t *testing.T, sharedStore auth.RefreshGenerationStore, user *jwtSharedStoreUser) *JWTScheme {
	t.Helper()
	cfg := auth.JWTConfig{
		Secret:                 strings.Repeat("s", 64),
		Algorithm:              "HS256",
		TTL:                    60,
		RefreshTTL:             20160,
		RefreshGenerationStore: sharedStore,
	}
	scheme, err := NewJWTScheme(&jwtSharedStoreUserStore{user: user}, cfg)
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}
	return scheme
}

// TestJWTScheme_RefreshGenerationStore_PropagatesAcrossSchemes is the F1
// regression test. Two JWTScheme instances pointing at the same
// RefreshGenerationStore simulate a multi-host fleet. Logout on host A
// MUST stale every refresh token outstanding for that user, including
// those tracked on host B.
//
// Without JWTConfig.RefreshGenerationStore (the operator-installable
// hook), each scheme kept its own in-memory counter and a Logout on host A
// did not propagate. A stolen refresh token would happily refresh on
// host B until its TTL expired (default 14 days).
func TestJWTScheme_RefreshGenerationStore_PropagatesAcrossSchemes(t *testing.T) {
	user := &jwtSharedStoreUser{id: "fleet-user"}
	shared := newSharedRefreshGenStore()

	hostA := newFleetScheme(t, shared, user)
	hostB := newFleetScheme(t, shared, user)

	// Host A mints a refresh token at generation 0.
	refreshToken, err := hostA.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	// Sanity: host B can refresh against the shared counter.
	if _, err := hostB.RefreshToken(refreshToken); err != nil {
		t.Fatalf("RefreshToken on host B pre-logout: %v", err)
	}

	// Host A Logout: must bump the shared counter.
	accessToken, err := hostA.GenerateToken(user)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.Header.Set("Authorization", "Bearer "+accessToken)
	if err := hostA.Logout(w, r); err != nil {
		t.Fatalf("Logout on host A: %v", err)
	}
	if got := shared.bumps.Load(); got < 1 {
		t.Fatalf("expected host-A Logout to bump shared counter; got %d bumps", got)
	}

	// Host B sees the bump via the shared store: the old refresh token
	// MUST now be rejected as stale even though host B never observed
	// a Logout call directly.
	_, err = hostB.RefreshToken(refreshToken)
	if err == nil {
		t.Fatal("RefreshToken on host B post-logout returned nil; expected ErrRefreshGenerationStale")
	}
	if !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Fatalf("expected ErrRefreshGenerationStale on host B, got %v", err)
	}
}

// TestJWTScheme_SetRefreshGenerationStore_Runtime confirms the setter
// hot-swaps the store after construction without re-creating the scheme.
// Used by modules that defer cache wiring to Start().
func TestJWTScheme_SetRefreshGenerationStore_Runtime(t *testing.T) {
	user := &jwtSharedStoreUser{id: "runtime-swap"}
	userStore := &jwtSharedStoreUserStore{user: user}

	scheme, err := NewJWTScheme(userStore, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}

	// Mint a refresh token against the default in-memory store.
	rt, err := scheme.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	// Swap to a fresh shared store. The previously issued refresh
	// token carries generation 0 against the OLD store. The new store
	// also reports generation 0 for the user (untouched), so a refresh
	// MUST still succeed: generation comparison is against the active
	// store, not the issuance-time store.
	shared := newSharedRefreshGenStore()
	scheme.SetRefreshGenerationStore(shared)

	if _, err := scheme.RefreshToken(rt); err != nil {
		t.Fatalf("RefreshToken post-swap pre-bump: %v", err)
	}

	// Bump on the new store; refresh must now fail.
	if _, err := shared.Bump("runtime-swap"); err != nil {
		t.Fatalf("Bump on shared store: %v", err)
	}
	_, err = scheme.RefreshToken(rt)
	if err == nil {
		t.Fatal("RefreshToken post-bump returned nil; expected ErrRefreshGenerationStale")
	}
	if !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Fatalf("expected ErrRefreshGenerationStale, got %v", err)
	}
}

// TestJWTScheme_SetRefreshGenerationStore_NilReverts confirms passing nil
// resets to the in-process default rather than panicking on a nil
// interface read inside RefreshToken.
func TestJWTScheme_SetRefreshGenerationStore_NilReverts(t *testing.T) {
	user := &jwtSharedStoreUser{id: "nil-revert"}
	scheme, err := NewJWTScheme(&jwtSharedStoreUserStore{user: user}, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}

	scheme.SetRefreshGenerationStore(nil)

	// Should still operate (in-memory default reinstalled).
	rt, err := scheme.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken after nil-reset: %v", err)
	}
	if _, err := scheme.RefreshToken(rt); err != nil {
		t.Fatalf("RefreshToken after nil-reset: %v", err)
	}
}

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *jwtSharedStoreUserStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *jwtSharedStoreUserStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *jwtSharedStoreUserStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
