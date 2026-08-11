package guards

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
)

// errInvalidateSession wraps a real auth.Session but reports a
// caller-supplied error from Invalidate. The underlying session is
// still asked to invalidate (so its destroyed/data/id mutations land
// on the real object), then the caller-supplied error is returned in
// place of nil. Mirrors the rare crypto/rand failure path inside
// BaseSession.Invalidate where the session is marked destroyed and
// its id zeroed even though regenerate failed.
type errInvalidateSession struct {
	auth.Session
	err error
}

func (e *errInvalidateSession) Invalidate() error {
	_ = e.Session.Invalidate()
	return e.err
}

// TestSessionScheme_LogoutContinuesTeardownOnInvalidateError pins the
// invariant that an Invalidate() failure does NOT short-circuit the
// rest of Logout. Pre-fix the early return skipped Save (no delete
// cookie on the wire), CookieStore.Revoke (cookie still decrypts),
// and server-store Delete (live record survives). Post-fix every
// teardown step still runs against the pre-Invalidate sessionID and
// the Invalidate error is returned at the end.
func TestSessionScheme_LogoutContinuesTeardownOnInvalidateError(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("pre-logout server store: want 1 entry, got %d", len(list))
	}

	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest("POST", "/logout", nil)
	logoutR.AddCookie(cookie)
	logoutR = WithSessionContext(logoutR)

	// Resolve the real session, wrap it, and replace the cached
	// session on the request's sessionHolder so scheme.Logout
	// consumes the wrapped instance. getSession does not expose a
	// constructor hook, so injection has to land on the holder.
	real := scheme.getSession(logoutR)
	if real == nil {
		t.Fatal("scheme.getSession returned nil on logout request")
	}
	sentinel := errors.New("rand-source exhausted")
	wrapped := &errInvalidateSession{Session: real, err: sentinel}
	holder, ok := logoutR.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		t.Fatal("expected WithSessionContext to attach a *sessionHolder")
	}
	holder.setSession(wrapped)

	err := scheme.Logout(logoutW, logoutR)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Logout must surface the Invalidate error; got %v want %v", err, sentinel)
	}

	// Save() ran despite the Invalidate error: response carries a
	// delete cookie. A cookie deletion is signalled by MaxAge<0,
	// Expires in the past, or an empty Value (depending on store
	// emission shape).
	now := time.Now()
	var sawDelete bool
	for _, c := range logoutW.Result().Cookies() {
		if c.Name != "vel_session" {
			continue
		}
		if c.MaxAge < 0 || (!c.Expires.IsZero() && c.Expires.Before(now)) || c.Value == "" {
			sawDelete = true
			break
		}
	}
	if !sawDelete {
		t.Errorf("response did not carry a delete cookie for vel_session after Logout (cookies: %+v)", logoutW.Result().Cookies())
	}

	// Server-store Delete ran despite the Invalidate error.
	list, _ = store.ListForUser(context.Background(), "u1")
	if len(list) != 0 {
		t.Errorf("server-store Delete did not run on Invalidate-error path; want 0 entries, got %d", len(list))
	}
}
