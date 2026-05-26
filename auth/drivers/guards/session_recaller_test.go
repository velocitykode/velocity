package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// rememberRevivalProvider exposes UpdateRememberToken so the test can mint
// a valid remember cookie via Login + Save, then drive the revival path.
type rememberRevivalProvider struct {
	user *revokeTestUser
}

func (p *rememberRevivalProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.user == nil {
		return nil, nil
	}
	wantID, _ := p.user.GetAuthIdentifier().(string)
	gotID, _ := id.(string)
	if wantID != "" && wantID == gotID {
		return p.user, nil
	}
	return nil, nil
}

func (p *rememberRevivalProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}

func (p *rememberRevivalProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}

func (p *rememberRevivalProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	if u, ok := user.(*revokeTestUser); ok {
		u.rememberToken = token
	}
	if p.user != nil {
		p.user.rememberToken = token
	}
	return nil
}

// mintRememberCookie returns the remember cookie set by a fresh Login that
// passed remember=true. The session cookie is discarded so the revival path
// runs with ONLY the remember cookie present.
func mintRememberCookie(t *testing.T, guard *SessionGuard) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r = WithSessionContext(r)
	if err := guard.Login(w, r, &revokeTestUser{id: "u1"}, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "remember_vel_session" {
			return c
		}
	}
	t.Fatal("no remember cookie issued")
	return nil
}

// failingServerStore is an auth.ServerSessionStore whose Put / Get always
// error. The H-08 revival path must refuse to authenticate when the store
// is configured but the just-written record cannot be retrieved.
type failingServerStore struct{}

var errStoreOffline = errors.New("test: store offline")

func (failingServerStore) Get(_ context.Context, _ string) (*auth.StoredSession, error) {
	return nil, errStoreOffline
}
func (failingServerStore) Put(_ context.Context, _ *auth.StoredSession) error { return errStoreOffline }
func (failingServerStore) Delete(_ context.Context, _ string) error           { return nil }
func (failingServerStore) DeleteAllForUser(_ context.Context, _ string) error { return nil }
func (failingServerStore) ListForUser(_ context.Context, _ string) ([]*auth.SessionMeta, error) {
	return nil, nil
}

// TestSessionGuard_RememberRevival_FailsWhenStoreUnreachable is the H-08
// regression test. With a server-side store wired AND the revival path
// matching (valid remember cookie, no session user_id), if the store
// write/read fails the user MUST NOT be returned. Pre-fix the recaller
// silently anchored user_id into the in-memory session and returned the
// user, granting one fully authenticated request despite the store being
// the authoritative source of truth.
func TestSessionGuard_RememberRevival_FailsWhenStoreUnreachable(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	rememberCookie := mintRememberCookie(t, guard)

	// Install the failing store AFTER Login so the remember cookie
	// itself was minted successfully. The revival now has to write
	// against the broken store; both Put and Get will error.
	guard.SetServerSessionStore(failingServerStore{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(rememberCookie)
	req = WithSessionContext(req)

	if u := guard.User(req); u != nil {
		t.Fatalf("User(req) returned %v; expected nil when revival cannot persist to store", u)
	}
	if guard.Check(req) {
		t.Fatal("Check(req) returned true; expected false when revival cannot persist to store")
	}
}

// TestSessionGuard_RememberRevival_RotatesSessionID pins the
// session-fixation defense: a request that arrives with a planted
// session id PLUS a valid remember cookie must end up on a fresh
// rotated id, not the planted one.
func TestSessionGuard_RememberRevival_RotatesSessionID(t *testing.T) {
	// No server-side store here; the rotation check applies regardless.
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	rememberCookie := mintRememberCookie(t, guard)

	// Capture the pre-revival session id by loading the session via
	// the guard's normal path (no user_id yet). We then rerun User()
	// which should observe the remember cookie and rotate the id.
	preReq := httptest.NewRequest(http.MethodGet, "/", nil)
	preReq.AddCookie(rememberCookie)
	preReq = WithSessionContext(preReq)
	preSession := guard.getSession(preReq)
	preID := ""
	if preSession != nil {
		preID = preSession.ID()
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(rememberCookie)
	req = WithSessionContext(req)

	if u := guard.User(req); u == nil {
		t.Fatal("User(req) returned nil; expected revival to succeed without server store")
	}

	holder, ok := req.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		t.Fatal("session holder unexpectedly nil after revival")
	}
	sess := holder.getSession()
	if sess == nil {
		t.Fatal("session holder.getSession() returned nil after revival")
	}
	postID := sess.ID()
	if postID == "" {
		t.Fatal("revived session id is empty; expected rotated id")
	}
	if postID == preID && preID != "" {
		t.Fatalf("revived session id %q == pre-revival id; expected rotation", postID)
	}
	if sess.Get("user_id") == nil {
		t.Fatal("revived session has no user_id anchored")
	}
}
