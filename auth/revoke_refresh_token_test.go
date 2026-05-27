package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRefreshRevokerGuard implements Guard + RefreshTokenRevoker so the
// test can observe whether Manager.RevokeAllSessions reaches the refresh
// revocation path. The Guard methods are no-ops; only the bookkeeping
// matters.
type fakeRefreshRevokerGuard struct {
	calls atomic.Int32
	last  atomic.Value // string
	err   error
}

func (g *fakeRefreshRevokerGuard) RevokeAllRefreshTokensForUser(_ context.Context, userID string) error {
	g.calls.Add(1)
	g.last.Store(userID)
	return g.err
}

// Stub Guard methods - RevokeAllSessions only inspects the
// RefreshTokenRevoker interface; the rest can be panics or no-ops.
func (g *fakeRefreshRevokerGuard) Check(*http.Request) bool           { return false }
func (g *fakeRefreshRevokerGuard) User(*http.Request) Authenticatable { return nil }
func (g *fakeRefreshRevokerGuard) ID(*http.Request) interface{}       { return nil }
func (g *fakeRefreshRevokerGuard) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *fakeRefreshRevokerGuard) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *fakeRefreshRevokerGuard) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
func (g *fakeRefreshRevokerGuard) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (g *fakeRefreshRevokerGuard) SetProvider(UserProvider)                        {}

// TestManager_RevokeAllSessions_WalksRefreshRevoker covers audit M-10:
// the previous walk only invoked RememberTokenClearer, so guards backing
// JWT/bearer-token auth (which implement RefreshTokenRevoker but not
// RememberTokenClearer) were missed and outstanding refresh tokens
// survived the administrative purge for up to RefreshTTL (default 14d).
func TestManager_RevokeAllSessions_WalksRefreshRevoker(t *testing.T) {
	m := NewManager()
	m.SetServerSessionStore(newFakeServerSessionStore())

	g := &fakeRefreshRevokerGuard{}
	m.RegisterGuard("jwt", g)

	if err := m.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions err = %v", err)
	}
	if got := g.calls.Load(); got != 1 {
		t.Fatalf("RevokeAllRefreshTokensForUser called %d times, want 1", got)
	}
	if got, _ := g.last.Load().(string); got != "u1" {
		t.Fatalf("RevokeAllRefreshTokensForUser userID = %q, want u1", got)
	}
}

// TestManager_RevokeAllSessions_RefreshRevokeFailureWrapped pins the
// best-effort contract: a RevokeAllRefreshTokensForUser failure does not
// undo the store-side deletion but surfaces as ErrRememberClearPartial
// so admins can detect and retry.
func TestManager_RevokeAllSessions_RefreshRevokeFailureWrapped(t *testing.T) {
	m := NewManager()
	store := newFakeServerSessionStore()
	m.SetServerSessionStore(store)
	now := time.Now()
	_ = store.Put(context.Background(), &StoredSession{ID: "x", UserID: "u1", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)})

	g := &fakeRefreshRevokerGuard{err: errors.New("transport down")}
	m.RegisterGuard("jwt", g)

	err := m.RevokeAllSessions(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected partial error, got nil")
	}
	if !errors.Is(err, ErrRememberClearPartial) {
		t.Errorf("expected ErrRememberClearPartial, got %v", err)
	}
	// The store-side deletion still succeeded.
	if list, _ := store.ListForUser(context.Background(), "u1"); len(list) != 0 {
		t.Fatalf("u1 sessions remain after partial revocation: %d", len(list))
	}
}
