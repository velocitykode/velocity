package guards

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// rememberClearingProvider exposes the UpdateRememberToken call so the test
// can assert Logout cycled the persisted token.
type rememberClearingProvider struct {
	user    auth.Authenticatable
	updates []string
	updated int32
}

func (p *rememberClearingProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
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

func (p *rememberClearingProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}
func (p *rememberClearingProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *rememberClearingProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
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

// TestSessionGuard_Logout_ClearsRememberToken is the H-06 regression test.
// Logout MUST call UpdateRememberToken(user, "") for the authenticated user
// so a previously issued remember cookie cannot be replayed after sign-out.
func TestSessionGuard_Logout_ClearsRememberToken(t *testing.T) {
	user := &rememberClearingUser{id: "u-42", rememberToken: "stored-hash"}
	provider := &rememberClearingProvider{user: user}
	guard, _ := newRevokeGuard(t, nil)
	// Swap the provider so we can observe UpdateRememberToken calls.
	guard.SetProvider(provider)

	// Log the user in so a session cookie exists carrying user_id.
	loginW := httptest.NewRecorder()
	loginR := httptest.NewRequest(http.MethodPost, "/login", nil)
	loginR = WithSessionContext(loginR)
	if err := guard.Login(loginW, loginR, user); err != nil {
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
	if err := guard.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if got := atomic.LoadInt32(&provider.updated); got < 1 {
		t.Fatalf("expected UpdateRememberToken to be called, got %d", got)
	}
	if user.rememberToken != "" {
		t.Fatalf("remember_token after Logout = %q; want empty string", user.rememberToken)
	}
	// Most recent update must be the empty string.
	if last := provider.updates[len(provider.updates)-1]; last != "" {
		t.Fatalf("last UpdateRememberToken value = %q; want empty string", last)
	}
}

// TestSessionGuard_Logout_NoUserInSessionSkipsClear pins the defensive branch:
// when the session has no user_id (already-anonymous logout), the remember
// token clearing path is skipped without erroring.
func TestSessionGuard_Logout_NoUserInSessionSkipsClear(t *testing.T) {
	provider := &rememberClearingProvider{}
	guard, _ := newRevokeGuard(t, nil)
	guard.SetProvider(provider)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r = WithSessionContext(r)
	if err := guard.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if got := atomic.LoadInt32(&provider.updated); got != 0 {
		t.Fatalf("expected no UpdateRememberToken call for anonymous Logout, got %d", got)
	}
}
