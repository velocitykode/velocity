package guards

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/crypto"
)

// revokeTestUser is the minimal Authenticatable used by the revocation
// suite. The provider returns the same instance for any id, so tests can
// share a single user id across cookies.
type revokeTestUser struct {
	id            string
	rememberToken string
}

func (u *revokeTestUser) GetAuthIdentifier() interface{} { return u.id }
func (u *revokeTestUser) GetAuthPassword() string        { return "" }
func (u *revokeTestUser) GetRememberToken() string       { return u.rememberToken }
func (u *revokeTestUser) SetRememberToken(t string)      { u.rememberToken = t }

type revokeTestProvider struct {
	users map[string]*revokeTestUser
}

func (p *revokeTestProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	key, _ := id.(string)
	if u, ok := p.users[key]; ok {
		return u, nil
	}
	return nil, errors.New("not found")
}
func (p *revokeTestProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, errors.New("unused")
}
func (p *revokeTestProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *revokeTestProvider) UpdateRememberToken(user auth.Authenticatable, token string) error {
	// Propagate the token to the canonical user in the map so subsequent
	// FindByID reflects it. checkRememberCookie compares against the
	// stored token, so without this propagation the remember-me flow
	// cannot succeed in tests.
	id, _ := user.GetAuthIdentifier().(string)
	if u, ok := p.users[id]; ok {
		u.rememberToken = token
	}
	return nil
}

// CompareAndSwapRememberToken implements the capability the guard now
// requires for recall rotation; recalls fail closed without it.
func (p *revokeTestProvider) CompareAndSwapRememberToken(_ context.Context, user auth.Authenticatable, oldToken, newToken string) (bool, error) {
	id, _ := user.GetAuthIdentifier().(string)
	u, ok := p.users[id]
	if !ok || u.rememberToken != oldToken {
		return false, nil
	}
	return true, p.UpdateRememberToken(user, newToken)
}

// newRevokeGuard constructs a SessionGuard backed by a real cookie store
// and AES-256-GCM encryptor. When store is non-nil it is installed via
// SetServerSessionStore.
func newRevokeGuard(t *testing.T, store auth.ServerSessionStore) (*SessionGuard, *revokeTestProvider) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	provider := &revokeTestProvider{users: map[string]*revokeTestUser{
		"u1": {id: "u1"},
		"u2": {id: "u2"},
	}}
	guard, err := NewSessionGuard(provider, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	if store != nil {
		guard.SetServerSessionStore(store)
	}
	return guard, provider
}

// loginAndCookie performs Login for userID and returns the resulting
// session cookie. The request goes through WithSessionContext so the
// holder cache mirrors production middleware behavior.
func loginAndCookie(t *testing.T, guard *SessionGuard, userID string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "test-agent/1.0")
	r = WithSessionContext(r)
	if err := guard.Login(w, r, &revokeTestUser{id: userID}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "vel_session" {
			return c
		}
	}
	t.Fatal("no session cookie issued")
	return nil
}

// requestWith returns a fresh request bearing cookie, threaded through
// WithSessionContext so cached lookups are scoped per call.
func requestWith(cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	return WithSessionContext(r)
}

// TestSessionGuard_RevokeTakesEffectOnNextRequest is the regression
// guard for the velship Wave 3 #5a bug: a Manager.RevokeSession call
// must invalidate the matching cookie on the very next request.
func TestSessionGuard_RevokeTakesEffectOnNextRequest(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	guard, _ := newRevokeGuard(t, store)
	cookie := loginAndCookie(t, guard, "u1")

	// Sanity: Check passes before revoke.
	if !guard.Check(requestWith(cookie)) {
		t.Fatal("Check must pass for fresh cookie")
	}

	// Find the stored session id by listing.
	list, err := store.ListForUser(context.Background(), "u1")
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 stored session, got %d err=%v", len(list), err)
	}
	if err := store.Delete(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Bool path: Check must return false.
	if guard.Check(requestWith(cookie)) {
		t.Fatal("Check must return false after revocation")
	}

	// Sentinel path: CheckWithError must surface ErrSessionRevoked.
	ok, err := guard.CheckWithError(requestWith(cookie))
	if ok {
		t.Fatal("CheckWithError ok=true after revocation")
	}
	if !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked, got %v", err)
	}

	// User must also return nil.
	if u := guard.User(requestWith(cookie)); u != nil {
		t.Fatalf("User returned %v after revocation", u)
	}
}

// TestSessionGuard_SignOutEverywhere verifies DeleteAllForUser removes
// every session belonging to a user, leaving sessions of other users
// untouched.
func TestSessionGuard_SignOutEverywhere(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	guard, _ := newRevokeGuard(t, store)

	cookieA := loginAndCookie(t, guard, "u1")
	cookieB := loginAndCookie(t, guard, "u1")
	cookieC := loginAndCookie(t, guard, "u2")

	for _, c := range []*http.Cookie{cookieA, cookieB, cookieC} {
		if !guard.Check(requestWith(c)) {
			t.Fatalf("baseline Check failed for %s", c.Value[:12])
		}
	}

	if err := store.DeleteAllForUser(context.Background(), "u1"); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}

	if guard.Check(requestWith(cookieA)) {
		t.Error("u1 cookieA must be revoked")
	}
	if guard.Check(requestWith(cookieB)) {
		t.Error("u1 cookieB must be revoked")
	}
	if !guard.Check(requestWith(cookieC)) {
		t.Error("u2 cookieC must remain valid")
	}
}

// TestSessionGuard_CookieOnlyFallback ensures that without a server
// store installed the guard never returns ErrSessionRevoked and continues
// to authenticate purely from the cookie.
func TestSessionGuard_CookieOnlyFallback(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil) // no store
	cookie := loginAndCookie(t, guard, "u1")

	if !guard.Check(requestWith(cookie)) {
		t.Fatal("Check must pass cookie-only")
	}
	ok, err := guard.CheckWithError(requestWith(cookie))
	if !ok {
		t.Fatalf("CheckWithError ok=false err=%v", err)
	}
	if err != nil {
		t.Fatalf("CheckWithError must not error in cookie-only mode, got %v", err)
	}
}

// TestSessionGuard_LastSeenAtRefresh verifies that the debounce window is
// respected: a second Check inside the window does not advance LastSeenAt,
// while a Check after the window does. We drive this by mutating the
// stored record directly to simulate elapsed time without sleeping.
func TestSessionGuard_LastSeenAtRefresh(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	guard, _ := newRevokeGuard(t, store)
	cookie := loginAndCookie(t, guard, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	id := list[0].ID
	first := list[0].LastSeenAt

	// Inside-window Check: LastSeenAt must not change (debounce).
	if !guard.Check(requestWith(cookie)) {
		t.Fatal("Check failed")
	}
	list, _ = store.ListForUser(context.Background(), "u1")
	if !list[0].LastSeenAt.Equal(first) {
		t.Errorf("LastSeenAt advanced inside debounce window: %v -> %v", first, list[0].LastSeenAt)
	}

	// Backdate LastSeenAt past the window, then Check should refresh.
	rec, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	rec.LastSeenAt = time.Now().Add(-2 * lastSeenDebounce)
	if err := store.Put(context.Background(), rec); err != nil {
		t.Fatalf("Put backdated: %v", err)
	}

	// Use a fresh request so the cached holder result does not short-circuit
	// the store lookup.
	if !guard.Check(requestWith(cookie)) {
		t.Fatal("Check failed post-backdate")
	}
	list, _ = store.ListForUser(context.Background(), "u1")
	if !list[0].LastSeenAt.After(rec.LastSeenAt) {
		t.Errorf("LastSeenAt should have advanced after debounce expired (was %v, still %v)", rec.LastSeenAt, list[0].LastSeenAt)
	}
}

// TestSessionGuard_ConcurrentCheckRevoke exercises the race between a
// goroutine revoking the session and another performing Check. No matter
// who wins, neither must panic and Check must never return true after
// the revoke is observed.
func TestSessionGuard_ConcurrentCheckRevoke(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	guard, _ := newRevokeGuard(t, store)
	cookie := loginAndCookie(t, guard, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session")
	}
	id := list[0].ID

	const N = 200
	var wg sync.WaitGroup
	var revoked atomic.Bool

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			ok := guard.Check(requestWith(cookie))
			if revoked.Load() && ok {
				t.Errorf("Check returned true after revoke was observed")
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		_ = store.Delete(context.Background(), id)
		revoked.Store(true)
	}()
	wg.Wait()

	if guard.Check(requestWith(cookie)) {
		t.Fatal("Check must return false post-revoke")
	}
}

// TestSessionGuard_TransientStoreErrorFailsClosed verifies that a generic
// store error (not Not-Found / Expired) is surfaced from CheckWithError
// and causes Check to return false.
func TestSessionGuard_TransientStoreErrorFailsClosed(t *testing.T) {
	flaky := &flakyStore{inner: session.NewMemoryStore()}
	defer flaky.inner.Close(context.Background())

	guard, _ := newRevokeGuard(t, flaky)
	cookie := loginAndCookie(t, guard, "u1")

	flaky.fail.Store(true)

	if guard.Check(requestWith(cookie)) {
		t.Fatal("Check must fail-closed on store error")
	}
	ok, err := guard.CheckWithError(requestWith(cookie))
	if ok {
		t.Fatal("ok=true on store error")
	}
	if err == nil || errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("expected wrapped non-revoked error, got %v", err)
	}
}

// flakyStore wraps a real store and forces Get to return a non-sentinel
// error when fail is set, exercising the fail-closed branch in
// consultServerStore.
type flakyStore struct {
	inner *session.MemoryStore
	fail  atomic.Bool
}

var errFlaky = errors.New("flaky: simulated store outage")

func (f *flakyStore) Get(ctx context.Context, id string) (*auth.StoredSession, error) {
	if f.fail.Load() {
		return nil, errFlaky
	}
	return f.inner.Get(ctx, id)
}
func (f *flakyStore) Put(ctx context.Context, s *auth.StoredSession) error {
	return f.inner.Put(ctx, s)
}
func (f *flakyStore) Delete(ctx context.Context, id string) error { return f.inner.Delete(ctx, id) }
func (f *flakyStore) DeleteAllForUser(ctx context.Context, userID string) error {
	return f.inner.DeleteAllForUser(ctx, userID)
}
func (f *flakyStore) ListForUser(ctx context.Context, userID string) ([]*auth.SessionMeta, error) {
	return f.inner.ListForUser(ctx, userID)
}

// TestSessionGuard_LogoutDeletesStoreRecord ensures Logout tears down the
// server-side row even though the cookie is also wiped client-side.
func TestSessionGuard_LogoutDeletesStoreRecord(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	guard, _ := newRevokeGuard(t, store)
	cookie := loginAndCookie(t, guard, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 stored session pre-logout, got %d", len(list))
	}

	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest("POST", "/logout", nil)
	logoutR.AddCookie(cookie)
	logoutR = WithSessionContext(logoutR)
	if err := guard.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	list, _ = store.ListForUser(context.Background(), "u1")
	if len(list) != 0 {
		t.Errorf("Logout must delete server-side session, got %d remaining", len(list))
	}
}

// TestManager_PropagatesStoreToRegisteredGuards covers the wiring path:
// Manager.SetServerSessionStore must call SetServerSessionStore on every
// registered guard that implements ServerSessionStoreReceiver.
func TestManager_PropagatesStoreToRegisteredGuards(t *testing.T) {
	mgr := auth.NewManager()
	guard, _ := newRevokeGuard(t, nil)
	mgr.RegisterGuard("web", guard)

	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	cookie := loginAndCookie(t, guard, "u1")
	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected guard to write to propagated store, got %d entries", len(list))
	}

	if err := store.Delete(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if guard.Check(requestWith(cookie)) {
		t.Fatal("guard must honor revocation via Manager-propagated store")
	}
}

// TestManager_RegisterGuardAfterStoreInstalled verifies the late-registration
// path: if the store is installed before the guard is registered, the guard
// still receives it.
func TestManager_RegisterGuardAfterStoreInstalled(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	guard, _ := newRevokeGuard(t, nil)
	mgr.RegisterGuard("web", guard)

	cookie := loginAndCookie(t, guard, "u1")
	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("guard registered late did not receive store: %d entries", len(list))
	}
	_ = cookie
}

// loginAndCookies performs Login with remember=true and returns both the
// session and remember cookies issued.
func loginAndCookies(t *testing.T, guard *SessionGuard, userID string) (sess, rem *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "test-agent/1.0")
	r = WithSessionContext(r)
	if err := guard.Login(w, r, &revokeTestUser{id: userID}, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, c := range w.Result().Cookies() {
		switch c.Name {
		case "vel_session":
			sess = c
		case "remember_vel_session":
			rem = c
		}
	}
	if sess == nil || rem == nil {
		t.Fatalf("missing cookies: session=%v remember=%v", sess, rem)
	}
	return sess, rem
}

// requestWithRememberOnly returns a request carrying only the remember
// cookie, exercising the checkRememberCookie path (no session cookie).
// A recorder is installed as the response writer the way SessionMiddleware
// does, because recall success now requires delivering a rotated remember
// cookie (V2-08).
func requestWithRememberOnly(rem *http.Cookie) *http.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(rem)
	r = WithSessionContext(r)
	if holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder); ok && holder != nil {
		holder.setResponseWriter(httptest.NewRecorder())
	}
	return r
}

// TestManager_RevokeAllSessions_ClearsRememberToken verifies the Option C
// wiring: Manager.RevokeAllSessions also nukes the user's remember-me
// token via the registered guard's RememberTokenClearer impl, so the
// remember cookie alone can no longer resurrect a session on the next
// request.
func TestManager_RevokeAllSessions_ClearsRememberToken(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	guard, provider := newRevokeGuard(t, nil)
	mgr.RegisterGuard("web", guard)

	_, rem := loginAndCookies(t, guard, "u1")

	// Sanity: remember cookie alone authenticates before revocation.
	if !guard.Check(requestWithRememberOnly(rem)) {
		t.Fatal("baseline: remember cookie must authenticate before revoke")
	}
	if provider.users["u1"].rememberToken == "" {
		t.Fatal("baseline: remember token must be persisted on provider")
	}

	if err := mgr.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if got := provider.users["u1"].rememberToken; got != "" {
		t.Errorf("remember token must be cleared, got %q", got)
	}
	if guard.Check(requestWithRememberOnly(rem)) {
		t.Fatal("remember cookie must not authenticate after RevokeAllSessions")
	}
}

// TestManager_RevokeSession_LeavesRememberIntact pins the intentional
// design gap: single-session revoke must NOT touch the user's remember
// token (which is per-user, not per-session). A future "fix" that wipes
// remember on RevokeSession would silently log every device of the user
// out, which is surprising. Bumping this test catches that drift.
func TestManager_RevokeSession_LeavesRememberIntact(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	guard, provider := newRevokeGuard(t, nil)
	mgr.RegisterGuard("web", guard)

	_, rem := loginAndCookies(t, guard, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if err := mgr.RevokeSession(context.Background(), list[0].ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	if got := provider.users["u1"].rememberToken; got == "" {
		t.Error("remember token must remain intact after RevokeSession")
	}
	if !guard.Check(requestWithRememberOnly(rem)) {
		t.Error("remember cookie must still authenticate after single-session revoke")
	}
}

// TestRememberTokenClearer_UserNotFound asserts the clearer is silent
// when the user no longer exists. Manager.RevokeAllSessions calls this
// best-effort and a missing user is not an error worth surfacing.
func TestRememberTokenClearer_UserNotFound(t *testing.T) {
	guard, _ := newRevokeGuard(t, nil)
	if err := guard.ClearRememberTokensForUser(context.Background(), "ghost_id"); err != nil {
		t.Fatalf("ClearRememberTokensForUser must be no-op for missing user, got %v", err)
	}
}

// noReceiverGuard is a Guard that intentionally does NOT implement
// ServerSessionStoreReceiver or RememberTokenClearer. It pins the
// optional-interface contract: Manager must skip such guards silently
// rather than panic during type assertion.
type noReceiverGuard struct{}

func (noReceiverGuard) Check(*http.Request) bool                { return false }
func (noReceiverGuard) User(*http.Request) auth.Authenticatable { return nil }
func (noReceiverGuard) ID(*http.Request) interface{}            { return nil }
func (noReceiverGuard) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (noReceiverGuard) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (noReceiverGuard) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
func (noReceiverGuard) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (noReceiverGuard) SetProvider(auth.UserProvider)                   {}

// TestManager_GuardWithoutReceiverInterfaces_Skipped pins the type-assertion
// contract: a Guard that implements neither ServerSessionStoreReceiver nor
// RememberTokenClearer must be silently skipped by SetServerSessionStore
// and RevokeAllSessions. Catches a regression where a future refactor
// might call methods on a non-conforming guard and panic.
func TestManager_GuardWithoutReceiverInterfaces_Skipped(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	mgr.RegisterGuard("inert", noReceiverGuard{})

	// Setting the store must not panic and must not error.
	mgr.SetServerSessionStore(store)

	// RevokeAllSessions must succeed (store delete only, clearer skipped).
	if err := mgr.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions must not error for non-receiver guard, got %v", err)
	}
}

// failingClearerGuard wraps SessionGuard but overrides
// ClearRememberTokensForUser to always return an error, exercising the
// new ErrRememberClearPartial return path.
type failingClearerGuard struct {
	*SessionGuard
	err error
}

func (f *failingClearerGuard) ClearRememberTokensForUser(context.Context, string) error {
	return f.err
}

// TestManager_RevokeAllSessions_ClearerErrorReturnsPartial verifies that
// when the store delete succeeds but a clearer fails, RevokeAllSessions
// returns ErrRememberClearPartial wrapping the underlying error so admin
// tooling can distinguish "session deleted, remember leaked" from full
// success.
func TestManager_RevokeAllSessions_ClearerErrorReturnsPartial(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	innerGuard, _ := newRevokeGuard(t, nil)
	clearerErr := errors.New("provider DB down")
	mgr.RegisterGuard("web", &failingClearerGuard{SessionGuard: innerGuard, err: clearerErr})

	// Seed a session so DeleteAllForUser has something to delete.
	_, _ = loginAndCookies(t, innerGuard, "u1")

	err := mgr.RevokeAllSessions(context.Background(), "u1")
	if err == nil {
		t.Fatal("expected ErrRememberClearPartial, got nil")
	}
	if !errors.Is(err, auth.ErrRememberClearPartial) {
		t.Errorf("err must wrap ErrRememberClearPartial, got %v", err)
	}
	if !errors.Is(err, clearerErr) {
		t.Errorf("err must wrap underlying clearer error, got %v", err)
	}

	// Despite the clearer failure, the load-bearing action (store delete)
	// must have succeeded.
	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 0 {
		t.Errorf("store sessions must be deleted even when clearer fails, got %d", len(list))
	}
}

// Ctx-suffixed shims for auth.UserProvider, added in Sweep 1b.
func (p *revokeTestProvider) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *revokeTestProvider) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *revokeTestProvider) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
