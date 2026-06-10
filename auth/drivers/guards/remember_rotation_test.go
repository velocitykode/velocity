package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// rotationEncryptor mirrors the encryptor newRevokeGuard builds internally
// so tests can decrypt the remember cookies it mints.
func rotationEncryptor(t *testing.T) crypto.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	return enc
}

// rawRememberToken decrypts a remember cookie and returns the raw token
// half of the "userID|token" payload.
func rawRememberToken(t *testing.T, enc crypto.Encryptor, c *http.Cookie) string {
	t.Helper()
	decrypted, err := enc.Decrypt(c.Value)
	if err != nil {
		t.Fatalf("decrypt remember cookie: %v", err)
	}
	parts := strings.SplitN(decrypted, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("remember cookie payload %q not in userID|token form", decrypted)
	}
	return parts[1]
}

// rememberRecallRequest builds a request carrying only the remember cookie,
// with a sessionHolder whose response writer is installed, mirroring what
// SessionMiddleware does in production.
func rememberRecallRequest(t *testing.T, c *http.Cookie, w http.ResponseWriter) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	r = WithSessionContext(r)
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		t.Fatal("WithSessionContext did not install a holder")
	}
	holder.setResponseWriter(w)
	return r
}

// findRememberCookie returns the remember cookie from a recorder's response,
// or nil when none was set.
func findRememberCookie(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "remember_vel_session" {
			return c
		}
	}
	return nil
}

// TestRememberRecall_RotatesToken is the V2-08 regression test. A successful
// remember-cookie recall must mint a fresh token: the response carries a
// Set-Cookie with a different raw token, and the persisted hash is replaced
// so the presented token cannot be replayed.
func TestRememberRecall_RotatesToken(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)
	enc := rotationEncryptor(t)

	oldCookie := mintRememberCookie(t, guard)
	oldToken := rawRememberToken(t, enc, oldCookie)
	oldHash := provider.user.rememberToken

	w := httptest.NewRecorder()
	r := rememberRecallRequest(t, oldCookie, w)
	if u := guard.User(r); u == nil {
		t.Fatal("User(req) returned nil; expected recall to succeed")
	}

	newCookie := findRememberCookie(w)
	if newCookie == nil {
		t.Fatal("recall response carries no remember Set-Cookie; expected rotated token")
	}
	newToken := rawRememberToken(t, enc, newCookie)
	if newToken == oldToken {
		t.Fatal("rotated remember token equals the presented token; expected a fresh token")
	}
	if provider.user.rememberToken == oldHash {
		t.Fatal("persisted remember hash unchanged after recall; expected rotation to overwrite it")
	}
	if provider.user.rememberToken != hashRememberToken(newToken) {
		t.Error("persisted hash does not match the freshly minted token")
	}
}

// TestRememberRecall_OldTokenRejectedAfterRotation pins the replay defense:
// once a recall rotated the token, the previously presented cookie must no
// longer authenticate, while the replacement cookie must.
func TestRememberRecall_OldTokenRejectedAfterRotation(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)

	w := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, oldCookie, w)); u == nil {
		t.Fatal("first recall failed; cannot exercise replay")
	}
	newCookie := findRememberCookie(w)
	if newCookie == nil {
		t.Fatal("first recall did not rotate the cookie")
	}

	// Replay the old cookie on a fresh request: must be unauthenticated.
	replayW := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, oldCookie, replayW)); u != nil {
		t.Fatalf("replayed old remember cookie authenticated as %v; expected nil", u.GetAuthIdentifier())
	}
	if guard.Check(rememberRecallRequest(t, oldCookie, httptest.NewRecorder())) {
		t.Fatal("Check accepted the replayed old remember cookie")
	}

	// The replacement cookie keeps working (and rotates again).
	nextW := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, newCookie, nextW)); u == nil {
		t.Fatal("rotated remember cookie rejected; expected it to authenticate")
	}
	if findRememberCookie(nextW) == nil {
		t.Fatal("second recall did not rotate again")
	}
}

// TestRememberRecall_RotatedCookieKeepsFlags pins the cookie attributes:
// rotation reuses setRememberCookie, so the replacement cookie must carry
// the same Path/MaxAge/HttpOnly/Secure/SameSite as the login-minted one.
func TestRememberRecall_RotatedCookieKeepsFlags(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)

	w := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, oldCookie, w)); u == nil {
		t.Fatal("recall failed")
	}
	newCookie := findRememberCookie(w)
	if newCookie == nil {
		t.Fatal("no rotated cookie issued")
	}

	if newCookie.Path != oldCookie.Path {
		t.Errorf("Path = %q, want %q", newCookie.Path, oldCookie.Path)
	}
	if newCookie.MaxAge != oldCookie.MaxAge {
		t.Errorf("MaxAge = %d, want %d", newCookie.MaxAge, oldCookie.MaxAge)
	}
	if newCookie.HttpOnly != oldCookie.HttpOnly {
		t.Errorf("HttpOnly = %v, want %v", newCookie.HttpOnly, oldCookie.HttpOnly)
	}
	if newCookie.Secure != oldCookie.Secure {
		t.Errorf("Secure = %v, want %v", newCookie.Secure, oldCookie.Secure)
	}
	if newCookie.SameSite != oldCookie.SameSite {
		t.Errorf("SameSite = %v, want %v", newCookie.SameSite, oldCookie.SameSite)
	}
}

// TestRememberRecall_NoWriterFailsClosed pins the fail-closed contract for
// guard calls outside SessionMiddleware: with no response writer on the
// holder there is nowhere to deliver a replacement cookie, rotation cannot
// complete, and the recall must be rejected, without burning the stored
// token and without leaving user_id anchored on the in-memory session.
// The same cookie then authenticates through a properly wired request.
func TestRememberRecall_NoWriterFailsClosed(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)
	oldHash := provider.user.rememberToken

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(oldCookie)
	r = WithSessionContext(r)
	if u := guard.User(r); u != nil {
		t.Fatalf("recall without response writer authenticated as %v; expected fail-closed nil", u.GetAuthIdentifier())
	}
	if guard.Check(r) {
		t.Fatal("Check accepted a recall that could not rotate the remember token")
	}
	if provider.user.rememberToken != oldHash {
		t.Fatal("stored hash rotated despite the rejected recall")
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if sess := holder.getSession(); sess != nil {
			if uid := sess.Get("user_id"); uid != nil {
				t.Errorf("user_id = %v anchored after rejected recall, want nil", uid)
			}
		}
	}

	// The same cookie still authenticates on a writer-equipped request.
	w := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, oldCookie, w)); u == nil {
		t.Fatal("cookie rejected after writer-less recall; token must not have been burned")
	}
}

// casRememberProvider layers call counting on top of
// rememberRevivalProvider so tests can drive the rotate-on-use race
// deterministically. plainCalls counts unconditional updates (the login
// path only); casCalls counts swap attempts.
type casRememberProvider struct {
	*rememberRevivalProvider
	casCalls   int
	plainCalls int
	forceStale bool
}

var _ auth.RememberTokenCompareAndSwapper = (*casRememberProvider)(nil)

func (p *casRememberProvider) UpdateRememberToken(u auth.Authenticatable, tok string) error {
	p.plainCalls++
	return p.rememberRevivalProvider.UpdateRememberToken(u, tok)
}

func (p *casRememberProvider) UpdateRememberTokenCtx(_ context.Context, u auth.Authenticatable, tok string) error {
	return p.UpdateRememberToken(u, tok)
}

func (p *casRememberProvider) CompareAndSwapRememberToken(_ context.Context, u auth.Authenticatable, oldToken, newToken string) (bool, error) {
	p.casCalls++
	if p.forceStale || p.user == nil || p.user.rememberToken != oldToken {
		return false, nil
	}
	return true, p.rememberRevivalProvider.UpdateRememberToken(u, newToken)
}

// TestRememberRecall_PrefersCompareAndSwap pins that rotation persists via
// the provider's CompareAndSwapRememberToken capability when present: the
// recall succeeds, exactly one swap runs, and no unconditional update is
// issued beyond the one Login performed when minting the cookie.
func TestRememberRecall_PrefersCompareAndSwap(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &casRememberProvider{rememberRevivalProvider: &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}}
	guard.SetProvider(provider)
	enc := rotationEncryptor(t)

	oldCookie := mintRememberCookie(t, guard)
	loginUpdates := provider.plainCalls

	w := httptest.NewRecorder()
	if u := guard.User(rememberRecallRequest(t, oldCookie, w)); u == nil {
		t.Fatal("recall with CAS-capable provider failed; expected success")
	}
	if provider.casCalls != 1 {
		t.Errorf("CompareAndSwapRememberToken calls = %d, want 1", provider.casCalls)
	}
	if provider.plainCalls != loginUpdates {
		t.Errorf("unconditional updates during recall = %d, want 0", provider.plainCalls-loginUpdates)
	}

	newCookie := findRememberCookie(w)
	if newCookie == nil {
		t.Fatal("recall response carries no rotated remember cookie")
	}
	if provider.user.rememberToken != hashRememberToken(rawRememberToken(t, enc, newCookie)) {
		t.Error("persisted hash does not match the swapped-in token")
	}
}

// TestRememberRecall_StaleSwapFailsClosed pins the parallel-recall race
// defense: the presented token validates, but a concurrent rotation
// replaces the stored hash before this request's compare-and-swap lands
// (simulated via forceStale). The losing request must be rejected, with
// no user_id anchored, instead of minting a second valid credential via
// last-writer-wins.
func TestRememberRecall_StaleSwapFailsClosed(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &casRememberProvider{rememberRevivalProvider: &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)
	oldHash := provider.user.rememberToken
	provider.forceStale = true

	w := httptest.NewRecorder()
	r := rememberRecallRequest(t, oldCookie, w)
	if u := guard.User(r); u != nil {
		t.Fatalf("stale-swap recall authenticated as %v; expected fail-closed nil", u.GetAuthIdentifier())
	}
	if guard.Check(rememberRecallRequest(t, oldCookie, httptest.NewRecorder())) {
		t.Fatal("Check accepted a recall whose compare-and-swap reported stale")
	}
	if provider.user.rememberToken != oldHash {
		t.Error("stored hash mutated by a losing swap; the winner's credential must stand")
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if sess := holder.getSession(); sess != nil {
			if uid := sess.Get("user_id"); uid != nil {
				t.Errorf("user_id = %v anchored after losing the rotation race, want nil", uid)
			}
		}
	}
}

// nonCASProvider hides any compare-and-swap capability behind the plain
// UserProvider interface: the wrapper's method set carries only the
// embedded interface's methods, so the guard's capability assertion fails.
type nonCASProvider struct {
	auth.UserProvider
}

// TestRememberRecall_NonCASProviderFailsClosed pins that recall rotation
// requires the atomic swap: a provider without
// auth.RememberTokenCompareAndSwapper must fail every recall closed
// instead of downgrading to an unconditional last-writer-wins update.
// Login-time issuance (no prior token consumed) must keep working.
func TestRememberRecall_NonCASProviderFailsClosed(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	base := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(nonCASProvider{base})

	oldCookie := mintRememberCookie(t, guard)
	oldHash := base.user.rememberToken

	w := httptest.NewRecorder()
	r := rememberRecallRequest(t, oldCookie, w)
	if u := guard.User(r); u != nil {
		t.Fatalf("recall authenticated as %v with a non-CAS provider; expected fail-closed nil", u.GetAuthIdentifier())
	}
	if base.user.rememberToken != oldHash {
		t.Error("stored hash mutated; non-CAS recall must not fall back to an unconditional update")
	}
	if c := findRememberCookie(w); c != nil {
		t.Error("rotated remember cookie issued despite fail-closed recall")
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if sess := holder.getSession(); sess != nil {
			if uid := sess.Get("user_id"); uid != nil {
				t.Errorf("user_id = %v anchored after fail-closed recall, want nil", uid)
			}
		}
	}
}

// failingUpdateProvider errors on every remember-token persist once armed,
// after Login has minted the cookie successfully.
type failingUpdateProvider struct {
	*rememberRevivalProvider
	failUpdates bool
}

func (p *failingUpdateProvider) UpdateRememberToken(u auth.Authenticatable, tok string) error {
	if p.failUpdates {
		return errors.New("test: persist outage")
	}
	return p.rememberRevivalProvider.UpdateRememberToken(u, tok)
}

func (p *failingUpdateProvider) UpdateRememberTokenCtx(_ context.Context, u auth.Authenticatable, tok string) error {
	return p.UpdateRememberToken(u, tok)
}

// CompareAndSwapRememberToken shadows the embedded implementation so the
// armed outage also hits the swap path the guard uses during recall.
func (p *failingUpdateProvider) CompareAndSwapRememberToken(ctx context.Context, u auth.Authenticatable, oldToken, newToken string) (bool, error) {
	if p.failUpdates {
		return false, errors.New("test: persist outage")
	}
	return p.rememberRevivalProvider.CompareAndSwapRememberToken(ctx, u, oldToken, newToken)
}

// TestRememberRecall_PersistFailureFailsClosed pins fail-closed rotation
// when the compare-and-swap persist errors: the recall must be rejected
// rather than authenticating a request whose presented credential was
// never burned.
func TestRememberRecall_PersistFailureFailsClosed(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &failingUpdateProvider{rememberRevivalProvider: &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)
	oldHash := provider.user.rememberToken
	provider.failUpdates = true

	w := httptest.NewRecorder()
	r := rememberRecallRequest(t, oldCookie, w)
	if u := guard.User(r); u != nil {
		t.Fatalf("recall authenticated as %v despite persist failure; expected fail-closed nil", u.GetAuthIdentifier())
	}
	if provider.user.rememberToken != oldHash {
		t.Error("stored hash changed despite the persist error")
	}
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		if sess := holder.getSession(); sess != nil {
			if uid := sess.Get("user_id"); uid != nil {
				t.Errorf("user_id = %v anchored after failed rotation persist, want nil", uid)
			}
		}
	}

	// Once the outage clears, the unburned cookie authenticates again.
	provider.failUpdates = false
	if u := guard.User(rememberRecallRequest(t, oldCookie, httptest.NewRecorder())); u == nil {
		t.Fatal("cookie rejected after persist outage cleared; token must not have been burned")
	}
}

// TestRememberRecall_SingleRotationPerRequest pins that repeated guard
// reads on one request rotate at most once: after the first User() call
// anchors user_id into the session, subsequent calls take the session
// path and must not mint additional cookies.
func TestRememberRecall_SingleRotationPerRequest(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	provider := &rememberRevivalProvider{user: &revokeTestUser{id: "u1"}}
	guard.SetProvider(provider)

	oldCookie := mintRememberCookie(t, guard)

	w := httptest.NewRecorder()
	r := rememberRecallRequest(t, oldCookie, w)
	if u := guard.User(r); u == nil {
		t.Fatal("first User() failed")
	}
	if u := guard.User(r); u == nil {
		t.Fatal("second User() failed; rotation must not invalidate the in-flight request")
	}
	if !guard.Check(r) {
		t.Fatal("Check() failed after rotation on the same request")
	}

	count := 0
	for _, c := range w.Result().Cookies() {
		if c.Name == "remember_vel_session" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 remember Set-Cookie, got %d", count)
	}
}
