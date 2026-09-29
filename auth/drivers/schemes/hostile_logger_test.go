package schemes

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/internal/hostile"
)

// clearFailingUsers is a user store whose remember-token writes fail, so a
// Logout reaches its "clear remember token failed" warning.
type clearFailingUsers struct {
	*revokeTestStore
}

func (clearFailingUsers) UpdateRememberTokenCtx(context.Context, auth.Authenticatable, string) error {
	return errors.New("users table down")
}

// A Logout whose warning logger panics still ends the session: the
// warning is written in the middle of the teardown, and the revocations
// after it (the session invalidated, its server record deleted) must run
// whatever the logger does. A replayed copy of the signed-out cookie is
// refused.
func TestSessionScheme_LogoutWithAPanickingLoggerStillEndsTheSession(t *testing.T) {
	cfg := teardownConfig()
	records := session.NewMemoryStore()
	store, err := session.NewServerStore(cfg, records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	users := clearFailingUsers{&revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}}
	scheme, err := NewSessionScheme(users, cfg, teardownEncryptor(t), WithSessionStore(store))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	code := hostile.New(t, hostile.Panic, nil)
	scheme.SetLogger(hostile.NewLogger(code, hostile.Warn))

	b := newStoreBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	signedIn, ok := b.cookies["vel_session"]
	if !ok {
		t.Fatal("premise: no session cookie after sign-in")
	}
	if w := b.do(http.MethodGet, "/check"); w.Code != http.StatusOK {
		t.Fatalf("premise: signed-in check = %d", w.Code)
	}

	var w interface{ Result() *http.Response }
	if p := hostile.Within(t, hostile.Deadline, func() { w = b.do(http.MethodPost, "/logout") }); p != nil {
		t.Fatalf("Logout let the logger's panic out: %v", p)
	}
	if code.Calls() == 0 {
		t.Fatal("premise: the logout wrote no warning")
	}
	if got := w.Result().StatusCode; got != http.StatusOK {
		t.Fatalf("logout answered %d, want 200: the teardown was cut short", got)
	}

	b.cookies = map[string]*http.Cookie{"vel_session": signedIn}
	if w := b.do(http.MethodGet, "/check"); w.Code == http.StatusOK {
		t.Fatal("a replayed copy of the signed-out cookie is still signed in")
	}
}
