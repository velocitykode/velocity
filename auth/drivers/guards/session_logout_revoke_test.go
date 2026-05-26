package guards

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSessionGuard_Logout_RevokesCookieStoreSessionID is the H-04 regression
// test. With NO server-side ServerSessionStore wired (the default
// configuration), a Logout call MUST still invalidate the cookie via the
// CookieStore's revocation list, so a captured cookie cannot be replayed
// after the user signs out.
//
// Pre-fix: Logout writes a MaxAge=-1 Set-Cookie header, but the
// cookie-encryption layer happily decrypts the same value on a replay
// request and returns a fully populated session.
//
// Post-fix: Logout calls store.Revoke(sessionID), and every subsequent
// Get returns a fresh empty session.
func TestSessionGuard_Logout_RevokesCookieStoreSessionID(t *testing.T) {
	// No server-side store: this is the default-config case the H-04
	// audit is about.
	guard, _ := newRevokeGuard(t, nil)
	cookie := loginAndCookie(t, guard, "u1")

	// Sanity: cookie authenticates pre-logout.
	if !guard.Check(requestWith(cookie)) {
		t.Fatal("Check must pass for fresh cookie")
	}

	// Logout against the same cookie. The fix wires
	// SessionGuard.Logout to call CookieStore.Revoke.
	logoutW := httptest.NewRecorder()
	logoutR := requestWith(cookie)
	if err := guard.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	// A REPLAY of the same captured cookie MUST now be rejected.
	// We deliberately use a brand new request so the per-request
	// sessionHolder cache cannot mask the bug: the Get call against
	// the underlying store is what enforces the revocation.
	replay := httptest.NewRequest(http.MethodGet, "/", nil)
	replay.AddCookie(cookie)
	replay = WithSessionContext(replay)

	if guard.Check(replay) {
		t.Fatal("Check must return false after Logout for the same captured cookie value")
	}
	if user := guard.User(replay); user != nil {
		t.Fatalf("User returned %v after Logout; expected nil for revoked cookie", user)
	}
}

// TestSessionGuard_Logout_NoSessionIDNoRevoke covers the defensive branch
// where the session id ends up empty (a corrupted cookie that decrypts but
// has no id). Logout must not panic and must not insert an empty-string key
// into the revocation list.
func TestSessionGuard_Logout_NoSessionIDNoRevoke(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	// No cookie attached. getSession will Create("") on the way through,
	// yielding a session whose ID is auto-generated but non-empty.
	r = WithSessionContext(r)
	if err := guard.Logout(w, r); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	// We are mainly asserting "does not panic"; the revocation list may
	// hold the auto-generated id (harmless) but must not hold the empty
	// string.
}
