package guards

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// fakeCSRFRotator is a contract.CSRFTokenRotator that records every
// RotateToken / RevokeToken call. Tests use it to pin the pre/post
// session-id pairs the guard passes to the rotator across Login,
// Logout, and remember-cookie revival.
type fakeCSRFRotator struct {
	mu        sync.Mutex
	rotated   []rotateCall
	revoked   []string
	rotateErr error
}

type rotateCall struct {
	oldID, newID string
}

func (f *fakeCSRFRotator) RotateToken(oldID, newID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rotated = append(f.rotated, rotateCall{oldID: oldID, newID: newID})
	return f.rotateErr
}

func (f *fakeCSRFRotator) RevokeToken(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, id)
	return nil
}

// compile-time guarantee SessionGuard satisfies the propagation shape.
var _ interface {
	SetCSRFTokenRotator(contract.CSRFTokenRotator)
} = (*SessionGuard)(nil)

// TestSessionGuard_LoginRotatesCSRFToken pins the Login half of H-02.
// SessionGuard.Login MUST call rotator.RotateToken(oldID, newID) after
// Session.Regenerate so any CSRF token bound to the pre-login session
// id is dropped and the post-login id gets a fresh token. Pre-fix Login
// regenerated the session id without touching the CSRF store, leaving
// an orphan token reachable for the full token-store TTL (24h default)
// under an id an attacker may have planted before login.
func TestSessionGuard_LoginRotatesCSRFToken(t *testing.T) {
	rotator := &fakeCSRFRotator{}
	guard, _ := newRevokeGuard(t, nil)
	guard.SetCSRFTokenRotator(rotator)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req = WithSessionContext(req)
	w := httptest.NewRecorder()

	if err := guard.Login(w, req, &revokeTestUser{id: "u1"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	if got := len(rotator.rotated); got != 1 {
		t.Fatalf("expected exactly 1 RotateToken call, got %d", got)
	}
	call := rotator.rotated[0]
	if call.newID == "" {
		t.Errorf("RotateToken newID empty; the post-regenerate session id MUST be passed")
	}
	if call.oldID == call.newID {
		t.Error("RotateToken called with oldID == newID; orphan-deletion would silently no-op")
	}
}

// TestSessionGuard_LogoutRevokesCSRFToken pins the Logout half of H-02.
// SessionGuard.Logout MUST call rotator.RevokeToken(id) BEFORE
// Session.Invalidate destroys the bag, so the per-session CSRF token
// does not survive logout in the CSRF store. Pre-fix Logout invalidated
// the session without touching the CSRF store, so a captured
// cookie+token pair stayed valid for the store TTL past logout.
func TestSessionGuard_LogoutRevokesCSRFToken(t *testing.T) {
	rotator := &fakeCSRFRotator{}
	guard, _ := newRevokeGuard(t, nil)
	guard.SetCSRFTokenRotator(rotator)

	// Establish a real session via Login first so Logout has something
	// to tear down. Login's own rotation also goes through the rotator
	// (recorded as rotated[0]); the Logout revoke is rotator.revoked[0].
	loginReq := httptest.NewRequest(http.MethodPost, "/login", nil)
	loginReq = WithSessionContext(loginReq)
	loginW := httptest.NewRecorder()
	if err := guard.Login(loginW, loginReq, &revokeTestUser{id: "u1"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	// Reuse the same session context so Logout sees the same session.
	logoutW := httptest.NewRecorder()
	if err := guard.Logout(logoutW, loginReq); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if got := len(rotator.revoked); got != 1 {
		t.Fatalf("expected exactly 1 RevokeToken call, got %d", got)
	}
	if rotator.revoked[0] == "" {
		t.Error("RevokeToken called with empty id; the session id captured BEFORE Invalidate must be passed")
	}
}

// TestSessionGuard_LoginAbortsOnRotateFailure pins the rotation
// invariant: if the CSRF rotator returns an error during Login the call
// MUST fail rather than commit a session whose CSRF token is missing or
// orphaned. The user sees an error and no authenticated session is
// established.
func TestSessionGuard_LoginAbortsOnRotateFailure(t *testing.T) {
	rotator := &fakeCSRFRotator{rotateErr: errors.New("store outage")}
	guard, _ := newRevokeGuard(t, nil)
	guard.SetCSRFTokenRotator(rotator)

	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req = WithSessionContext(req)
	w := httptest.NewRecorder()

	err := guard.Login(w, req, &revokeTestUser{id: "u1"})
	if err == nil {
		t.Fatal("Login must return an error when csrf rotation fails")
	}
	if !errors.Is(err, rotator.rotateErr) {
		t.Errorf("expected wrapped rotator error, got %v", err)
	}
}

// TestSessionGuard_RememberRevival_RotatesCSRFToken pins the revival
// half of H-02. The recall path inside anchorRecalledUser MUST call
// rotator.RotateToken(oldID, newID) between Session.Regenerate and the
// user_id Put so any CSRF token an attacker planted under the pre-
// revival session id is dropped and the post-revival id has a fresh
// token. Both User() and CheckWithError() reach this path via G2's H-08
// helper; the CSRF rotation must run regardless of which entry point
// fires.
func TestSessionGuard_RememberRevival_RotatesCSRFToken(t *testing.T) {
	rotator := &fakeCSRFRotator{}
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)
	guard.SetCSRFTokenRotator(rotator)

	rememberCookie := mintRememberCookie(t, guard)
	// Login above also produced a rotation entry; discard it so the
	// revival assertion below is on a clean slate.
	rotator.rotated = nil

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(rememberCookie)
	req = WithSessionContext(req)

	// Capture pre-revival id by loading the session once (no user_id).
	preID := guard.getSession(req).ID()

	if u := guard.User(req); u == nil {
		t.Fatal("User(req) returned nil; revival must succeed without a server store")
	}

	if got := len(rotator.rotated); got != 1 {
		t.Fatalf("expected exactly 1 RotateToken call from revival, got %d", got)
	}
	call := rotator.rotated[0]
	if call.oldID != preID {
		t.Errorf("RotateToken oldID = %q, want pre-revival id %q", call.oldID, preID)
	}
	if call.newID == "" {
		t.Error("RotateToken newID empty; expected post-regenerate id")
	}
	if call.oldID == call.newID {
		t.Error("RotateToken called with oldID == newID; orphan-deletion would no-op")
	}
}

// TestSessionGuard_RememberRevival_AbortsOnRotateFailure pins fail-
// closed semantics on the revival path. When the rotator errors during
// recall the guard must refuse to authenticate the request, mirroring
// Login's abort-on-rotate-failure behavior. User and Check must BOTH
// return false / nil because both go through anchorRecalledUser.
func TestSessionGuard_RememberRevival_AbortsOnRotateFailure(t *testing.T) {
	rotator := &fakeCSRFRotator{}
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)
	guard.SetCSRFTokenRotator(rotator)

	rememberCookie := mintRememberCookie(t, guard)

	// Switch the rotator to error mode AFTER the Login above so the
	// remember cookie itself was minted successfully. Revival must
	// fail.
	rotator.rotateErr = errors.New("store outage")

	for _, name := range []string{"User", "Check"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.AddCookie(rememberCookie)
			req = WithSessionContext(req)

			switch name {
			case "User":
				if u := guard.User(req); u != nil {
					t.Fatalf("User must return nil when csrf rotation fails on revival, got %v", u)
				}
			case "Check":
				if guard.Check(req) {
					t.Fatal("Check must return false when csrf rotation fails on revival")
				}
			}

			// user_id MUST NOT have been committed when the rotation
			// aborted; revival must look unauthenticated end-to-end.
			holder, _ := req.Context().Value(sessionCtxKey{}).(*sessionHolder)
			if holder != nil && holder.session != nil {
				if uid := holder.session.Get("user_id"); uid != nil {
					t.Errorf("user_id = %v after aborted revival, want nil", uid)
				}
			}
		})
	}
}
