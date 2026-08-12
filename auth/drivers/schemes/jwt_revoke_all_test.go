package schemes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// TestManager_RevokeAllSessions_StalesJWTRefreshTokens is the audit M-10
// end-to-end regression test: when a JWT scheme is registered with the
// manager and the manager's RevokeAllSessions fires, refresh tokens for
// the user MUST be rejected as stale on the next refresh call. The fix
// added the RefreshTokenRevoker interface, implemented it on JWTScheme
// (BumpRefreshGeneration), and made RevokeAllSessions walk both
// RememberTokenClearer and RefreshTokenRevoker capabilities.
func TestManager_RevokeAllSessions_StalesJWTRefreshTokens(t *testing.T) {
	user := &jwtLogoutRefreshUser{id: "purged-user"}
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

	refreshToken, err := scheme.GenerateRefreshToken(user)
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	// Sanity: refresh works before the administrative revocation.
	if _, err := scheme.RefreshToken(refreshToken); err != nil {
		t.Fatalf("RefreshToken pre-revoke returned %v", err)
	}

	mgr := auth.NewManager()
	// A server-side session store is mandatory; the in-test fake from
	// the auth package is unexported, so we use a minimal local stub.
	mgr.SetServerSessionStore(stubSessionStore{})
	mgr.RegisterScheme("api", scheme)

	if err := mgr.RevokeAllSessions(context.Background(), "purged-user"); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	// The refresh token is now stale because the JWT scheme's
	// RefreshTokenRevoker bumped the per-user generation counter.
	if _, err := scheme.RefreshToken(refreshToken); err == nil {
		t.Fatal("RefreshToken post-revoke returned nil; expected ErrRefreshGenerationStale")
	} else if !errors.Is(err, auth.ErrRefreshGenerationStale) {
		t.Fatalf("expected ErrRefreshGenerationStale, got %v", err)
	}
}

// stubSessionStore is the smallest auth.ServerSessionStore that satisfies
// Manager.RevokeAllSessions's "store is configured" scheme. All methods
// are no-ops; the test does not care about the session-row side.
type stubSessionStore struct{}

func (stubSessionStore) Get(context.Context, string) (*auth.StoredSession, error) {
	return nil, auth.ErrSessionNotFound
}
func (stubSessionStore) Put(context.Context, *auth.StoredSession) error { return nil }
func (stubSessionStore) Delete(context.Context, string) error           { return nil }
func (stubSessionStore) DeleteAllForUser(context.Context, string) error { return nil }
func (stubSessionStore) ListForUser(context.Context, string) ([]*auth.SessionMeta, error) {
	return nil, nil
}
