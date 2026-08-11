package guards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// rememberClearingStore exposes the UpdateRememberToken call so the test
// can assert Logout cycled the persisted token.
type rememberClearingStore struct {
	user    auth.Authenticatable
	updates []string
	updated int32
}

func (p *rememberClearingStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	if p.user == nil {
		return nil, nil
	}
	// match on string identifier for determinism
	want, _ := p.user.GetAuthIdentifier().(string)
	got, _ := id.(string)
	if want != "" && want == got {
		return p.user, nil
	}
	return nil, nil
}

func (p *rememberClearingStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (p *rememberClearingStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *rememberClearingStore) UpdateRememberToken(user auth.Authenticatable, token string) error {
	p.updates = append(p.updates, token)
	atomic.AddInt32(&p.updated, 1)
	user.SetRememberToken(token)
	return nil
}

// rememberClearingUser tracks SetRememberToken calls on the user record.
type rememberClearingUser struct {
	id            string
	rememberToken string
}

func (u *rememberClearingUser) GetAuthIdentifier() interface{} { return u.id }
func (u *rememberClearingUser) GetAuthPassword() string        { return "" }
func (u *rememberClearingUser) GetRememberToken() string       { return u.rememberToken }
func (u *rememberClearingUser) SetRememberToken(t string)      { u.rememberToken = t }

// TestSessionScheme_Logout_ClearsRememberToken is the H-06 regression test.
// Logout MUST call UpdateRememberToken(user, "") for the authenticated user
// so a previously issued remember cookie cannot be replayed after sign-out.
func TestSessionScheme_Logout_ClearsRememberToken(t *testing.T) {
	user := &rememberClearingUser{id: "u-42", rememberToken: "stored-hash"}
	userStore := &rememberClearingStore{user: user}
	scheme, _ := newRevokeScheme(t, nil)
	// Swap the user store so we can observe UpdateRememberToken calls.
	scheme.SetUserStore(userStore)

	// Log the user in so a session cookie exists carrying user_id.
	loginW := httptest.NewRecorder()
	loginR := httptest.NewRequest(http.MethodPost, "/login", nil)
	loginR = WithSessionContext(loginR)
	if err := scheme.Login(loginW, loginR, user); err != nil {
		t.Fatalf("Login: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range loginW.Result().Cookies() {
		if c.Name == "vel_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie issued")
	}

	// Logout. The fix must observe user_id in the session, look up the
	// user, and clear the persisted remember token.
	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutR.AddCookie(cookie)
	logoutR = WithSessionContext(logoutR)
	if err := scheme.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if got := atomic.LoadInt32(&userStore.updated); got < 1 {
		t.Fatalf("expected UpdateRememberToken to be called, got %d", got)
	}
	if user.rememberToken != "" {
		t.Fatalf("remember_token after Logout = %q; want empty string", user.rememberToken)
	}
	// Most recent update must be the empty string.
	if last := userStore.updates[len(userStore.updates)-1]; last != "" {
		t.Fatalf("last UpdateRememberToken value = %q; want empty string", last)
	}
}

// TestSessionScheme_Logout_NoUserInSessionSkipsClear pins the defensive branch:
// when the session has no user_id (already-anonymous logout), the remember
// token clearing path is skipped without erroring.
func TestSessionScheme_Logout_NoUserInSessionSkipsClear(t *testing.T) {
	userStore := &rememberClearingStore{}
	scheme, _ := newRevokeScheme(t, nil)
	scheme.SetUserStore(userStore)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r = WithSessionContext(r)
	if err := scheme.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if got := atomic.LoadInt32(&userStore.updated); got != 0 {
		t.Fatalf("expected no UpdateRememberToken call for anonymous Logout, got %d", got)
	}
}

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *rememberClearingStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *rememberClearingStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *rememberClearingStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
