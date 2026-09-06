package schemes

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
// suite. The user store returns the same instance for any id, so tests can
// share a single user id across cookies.
type revokeTestUser struct {
	id            string
	rememberToken string
}

func (u *revokeTestUser) GetAuthIdentifier() interface{} { return u.id }
func (u *revokeTestUser) GetAuthPassword() string        { return "" }
func (u *revokeTestUser) GetRememberToken() string       { return u.rememberToken }
func (u *revokeTestUser) SetRememberToken(t string)      { u.rememberToken = t }

type revokeTestStore struct {
	users map[string]*revokeTestUser
}

func (p *revokeTestStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	key, _ := id.(string)
	if u, ok := p.users[key]; ok {
		return u, nil
	}
	return nil, errors.New("not found")
}
func (p *revokeTestStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, errors.New("unused")
}
func (p *revokeTestStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *revokeTestStore) UpdateRememberToken(user auth.Authenticatable, token string) error {
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

// CompareAndSwapRememberToken implements the capability the scheme now
// requires for recall rotation; recalls fail closed without it.
func (p *revokeTestStore) CompareAndSwapRememberToken(_ context.Context, user auth.Authenticatable, oldToken, newToken string) (bool, error) {
	id, _ := user.GetAuthIdentifier().(string)
	u, ok := p.users[id]
	if !ok || u.rememberToken != oldToken {
		return false, nil
	}
	return true, p.UpdateRememberToken(user, newToken)
}

// newRevokeScheme constructs a SessionScheme backed by a real cookie store
// and AES-256-GCM encryptor. When store is non-nil it is installed via
// SetServerSessionStore.
func newRevokeScheme(t *testing.T, store auth.ServerSessionStore) (*SessionScheme, *revokeTestStore) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	userStore := &revokeTestStore{users: map[string]*revokeTestUser{
		"u1": {id: "u1"},
		"u2": {id: "u2"},
	}}
	scheme, err := NewSessionScheme(userStore, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	if store != nil {
		scheme.SetServerSessionStore(store)
	}
	return scheme, userStore
}

// loginAndCookie performs Login for userID and returns the resulting
// session cookie. The request goes through WithSessionContext so the
// holder cache mirrors production middleware behavior.
func loginAndCookie(t *testing.T, scheme *SessionScheme, userID string) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "test-agent/1.0")
	r = WithSessionContext(r)
	if err := scheme.Login(w, r, &revokeTestUser{id: userID}); err != nil {
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

// TestSessionScheme_RevokeTakesEffectOnNextRequest is the regression
// scheme for the velship Wave 3 #5a bug: a Manager.RevokeSession call
// must invalidate the matching cookie on the very next request.
func TestSessionScheme_RevokeTakesEffectOnNextRequest(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")

	// Sanity: Check passes before revoke.
	if !scheme.Check(requestWith(cookie)) {
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
	if scheme.Check(requestWith(cookie)) {
		t.Fatal("Check must return false after revocation")
	}

	// Sentinel path: CheckWithError must surface ErrSessionRevoked.
	ok, err := scheme.CheckWithError(requestWith(cookie))
	if ok {
		t.Fatal("CheckWithError ok=true after revocation")
	}
	if !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("expected ErrSessionRevoked, got %v", err)
	}

	// User must also return nil.
	if u := scheme.User(requestWith(cookie)); u != nil {
		t.Fatalf("User returned %v after revocation", u)
	}
}

// TestSessionScheme_SignOutEverywhere verifies DeleteAllForUser removes
// every session belonging to a user, leaving sessions of other users
// untouched.
func TestSessionScheme_SignOutEverywhere(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)

	cookieA := loginAndCookie(t, scheme, "u1")
	cookieB := loginAndCookie(t, scheme, "u1")
	cookieC := loginAndCookie(t, scheme, "u2")

	for _, c := range []*http.Cookie{cookieA, cookieB, cookieC} {
		if !scheme.Check(requestWith(c)) {
			t.Fatalf("baseline Check failed for %s", c.Value[:12])
		}
	}

	if err := store.DeleteAllForUser(context.Background(), "u1"); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}

	if scheme.Check(requestWith(cookieA)) {
		t.Error("u1 cookieA must be revoked")
	}
	if scheme.Check(requestWith(cookieB)) {
		t.Error("u1 cookieB must be revoked")
	}
	if !scheme.Check(requestWith(cookieC)) {
		t.Error("u2 cookieC must remain valid")
	}
}

// TestSessionScheme_CookieOnlyFallback ensures that without a server
// store installed the scheme never returns ErrSessionRevoked and continues
// to authenticate purely from the cookie.
func TestSessionScheme_CookieOnlyFallback(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil) // no store
	cookie := loginAndCookie(t, scheme, "u1")

	if !scheme.Check(requestWith(cookie)) {
		t.Fatal("Check must pass cookie-only")
	}
	ok, err := scheme.CheckWithError(requestWith(cookie))
	if !ok {
		t.Fatalf("CheckWithError ok=false err=%v", err)
	}
	if err != nil {
		t.Fatalf("CheckWithError must not error in cookie-only mode, got %v", err)
	}
}

// TestSessionScheme_LastSeenAtRefresh verifies that the debounce window is
// respected: a second Check inside the window does not advance LastSeenAt,
// while a Check after the window does. We drive this by mutating the
// stored record directly to simulate elapsed time without sleeping.
func TestSessionScheme_LastSeenAtRefresh(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	id := list[0].ID
	first := list[0].LastSeenAt

	// Inside-window Check: LastSeenAt must not change (debounce).
	if !scheme.Check(requestWith(cookie)) {
		t.Fatal("Check failed")
	}
	list, _ = store.ListForUser(context.Background(), "u1")
	if !list[0].LastSeenAt.Equal(first) {
		t.Errorf("LastSeenAt advanced inside debounce window: %v -> %v", first, list[0].LastSeenAt)
	}

	// Backdate LastSeenAt past the window, then Check should refresh.
	// Touch, not Put: Put stamps LastSeenAt with the current time, so a
	// Put-based backdate would silently leave the record inside the window.
	backdated := time.Now().Add(-2 * lastSeenDebounce)
	if err := store.Touch(context.Background(), id, backdated); err != nil {
		t.Fatalf("Touch backdated: %v", err)
	}
	rec, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !rec.LastSeenAt.Equal(backdated) {
		t.Fatalf("backdate did not stick: %v", rec.LastSeenAt)
	}

	// Use a fresh request so the cached holder result does not short-circuit
	// the store lookup.
	if !scheme.Check(requestWith(cookie)) {
		t.Fatal("Check failed post-backdate")
	}
	list, _ = store.ListForUser(context.Background(), "u1")
	if !list[0].LastSeenAt.After(rec.LastSeenAt) {
		t.Errorf("LastSeenAt should have advanced after debounce expired (was %v, still %v)", rec.LastSeenAt, list[0].LastSeenAt)
	}
}

// revokeBetweenReadAndWriteStore wraps a real store and runs a hook after
// every successful Get, before the scheme's Touch write-back. Deleting the
// record in that hook reproduces the window where an administrative
// revocation lands between the per-request read and the debounced
// LastSeenAt refresh.
type revokeBetweenReadAndWriteStore struct {
	*session.MemoryStore
	afterGet func(rec *auth.StoredSession)
}

func (s *revokeBetweenReadAndWriteStore) Get(ctx context.Context, id string) (*auth.StoredSession, error) {
	rec, err := s.MemoryStore.Get(ctx, id)
	if err == nil && s.afterGet != nil {
		s.afterGet(rec)
	}
	return rec, err
}

// TestSessionScheme_RevokeBetweenReadAndRefresh_DeniesRequest covers the
// audit finding that the activity refresh used Put (create-or-replace) and
// so recreated a record deleted after the read. With Touch the refresh
// cannot insert, the request that lost the race is denied, and the cookie
// stays dead on the next request. Both single and bulk revocation paths.
func TestSessionScheme_RevokeBetweenReadAndRefresh_DeniesRequest(t *testing.T) {
	cases := []struct {
		name   string
		revoke func(ctx context.Context, store auth.ServerSessionStore, rec *auth.StoredSession)
	}{
		{"single", func(ctx context.Context, store auth.ServerSessionStore, rec *auth.StoredSession) {
			_ = store.Delete(ctx, rec.ID)
		}},
		{"bulk", func(ctx context.Context, store auth.ServerSessionStore, rec *auth.StoredSession) {
			_ = store.DeleteAllForUser(ctx, rec.UserID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := session.NewMemoryStore()
			defer inner.Close(context.Background())
			store := &revokeBetweenReadAndWriteStore{MemoryStore: inner}

			scheme, _ := newRevokeScheme(t, store)
			cookie := loginAndCookie(t, scheme, "u1")

			list, _ := inner.ListForUser(context.Background(), "u1")
			if len(list) != 1 {
				t.Fatalf("expected 1 session, got %d", len(list))
			}
			id := list[0].ID

			// Backdate LastSeenAt past the debounce so the next Check refreshes.
			if err := inner.Touch(context.Background(), id, time.Now().Add(-2*lastSeenDebounce)); err != nil {
				t.Fatalf("Touch backdated: %v", err)
			}

			// Revoke in the window between the read and the refresh write.
			var fired atomic.Bool
			store.afterGet = func(rec *auth.StoredSession) {
				if fired.CompareAndSwap(false, true) {
					tc.revoke(context.Background(), inner, rec)
				}
			}

			if scheme.Check(requestWith(cookie)) {
				t.Fatal("Check succeeded on the request that lost the race against revocation")
			}
			if !fired.Load() {
				t.Fatal("revocation hook never fired; test did not exercise the window")
			}

			// The refresh must not have recreated the record.
			if _, err := inner.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
				t.Fatalf("record resurrected by the activity refresh: %v", err)
			}
			if list, _ := inner.ListForUser(context.Background(), "u1"); len(list) != 0 {
				t.Fatalf("user still lists %d sessions after revocation", len(list))
			}

			// And the cookie stays denied on the next request.
			store.afterGet = nil
			if scheme.Check(requestWith(cookie)) {
				t.Fatal("revoked cookie accepted on the following request")
			}
		})
	}
}

// TestSessionScheme_LastSeenAtRefresh_UsesTouchNotPut pins the contract:
// the debounced activity write must go through Touch. A store whose Put
// fails loudly after login proves no Put is issued on the read path.
func TestSessionScheme_LastSeenAtRefresh_UsesTouchNotPut(t *testing.T) {
	inner := session.NewMemoryStore()
	defer inner.Close(context.Background())
	store := &putTrapStore{MemoryStore: inner}

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")
	list, _ := inner.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	backdated := time.Now().Add(-2 * lastSeenDebounce)
	if err := inner.Touch(context.Background(), list[0].ID, backdated); err != nil {
		t.Fatalf("Touch backdated: %v", err)
	}
	rec, _ := inner.Get(context.Background(), list[0].ID)

	store.armed.Store(true)
	if !scheme.Check(requestWith(cookie)) {
		t.Fatal("Check failed")
	}
	if store.puts.Load() != 0 {
		t.Fatalf("activity refresh issued %d Put call(s); must use Touch", store.puts.Load())
	}
	if store.touches.Load() != 1 {
		t.Fatalf("expected exactly 1 Touch, got %d", store.touches.Load())
	}
	after, _ := inner.Get(context.Background(), rec.ID)
	if !after.LastSeenAt.After(rec.LastSeenAt) {
		t.Fatalf("LastSeenAt not refreshed via Touch: %v -> %v", rec.LastSeenAt, after.LastSeenAt)
	}
}

// putTrapStore counts Put and Touch calls once armed (after login).
type putTrapStore struct {
	*session.MemoryStore
	armed   atomic.Bool
	puts    atomic.Int32
	touches atomic.Int32
}

func (s *putTrapStore) Put(ctx context.Context, rec *auth.StoredSession) error {
	if s.armed.Load() {
		s.puts.Add(1)
	}
	return s.MemoryStore.Put(ctx, rec)
}

func (s *putTrapStore) Touch(ctx context.Context, id string, lastSeen time.Time) error {
	if s.armed.Load() {
		s.touches.Add(1)
	}
	return s.MemoryStore.Touch(ctx, id, lastSeen)
}

// TestSessionScheme_ConcurrentCheckRevoke exercises the race between a
// goroutine revoking the session and another performing Check. No matter
// who wins, neither must panic and Check must never return true after
// the revoke is observed.
func TestSessionScheme_ConcurrentCheckRevoke(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")

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
			ok := scheme.Check(requestWith(cookie))
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

	if scheme.Check(requestWith(cookie)) {
		t.Fatal("Check must return false post-revoke")
	}
}

// TestSessionScheme_TransientStoreErrorFailsClosed verifies that a generic
// store error (not Not-Found / Expired) is surfaced from CheckWithError
// and causes Check to return false.
func TestSessionScheme_TransientStoreErrorFailsClosed(t *testing.T) {
	flaky := &flakyStore{inner: session.NewMemoryStore()}
	defer flaky.inner.Close(context.Background())

	scheme, _ := newRevokeScheme(t, flaky)
	cookie := loginAndCookie(t, scheme, "u1")

	flaky.fail.Store(true)

	if scheme.Check(requestWith(cookie)) {
		t.Fatal("Check must fail-closed on store error")
	}
	ok, err := scheme.CheckWithError(requestWith(cookie))
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
func (f *flakyStore) Touch(ctx context.Context, id string, lastSeen time.Time) error {
	return f.inner.Touch(ctx, id, lastSeen)
}
func (f *flakyStore) Delete(ctx context.Context, id string) error { return f.inner.Delete(ctx, id) }
func (f *flakyStore) DeleteAllForUser(ctx context.Context, userID string) error {
	return f.inner.DeleteAllForUser(ctx, userID)
}
func (f *flakyStore) ListForUser(ctx context.Context, userID string) ([]*auth.SessionMeta, error) {
	return f.inner.ListForUser(ctx, userID)
}

// TestSessionScheme_LogoutDeletesStoreRecord ensures Logout tears down the
// server-side row even though the cookie is also wiped client-side.
func TestSessionScheme_LogoutDeletesStoreRecord(t *testing.T) {
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	scheme, _ := newRevokeScheme(t, store)
	cookie := loginAndCookie(t, scheme, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 stored session pre-logout, got %d", len(list))
	}

	logoutW := httptest.NewRecorder()
	logoutR := httptest.NewRequest("POST", "/logout", nil)
	logoutR.AddCookie(cookie)
	logoutR = WithSessionContext(logoutR)
	if err := scheme.Logout(logoutW, logoutR); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	list, _ = store.ListForUser(context.Background(), "u1")
	if len(list) != 0 {
		t.Errorf("Logout must delete server-side session, got %d remaining", len(list))
	}
}

// TestManager_PropagatesStoreToRegisteredSchemes covers the wiring path:
// Manager.SetServerSessionStore must call SetServerSessionStore on every
// registered scheme that implements ServerSessionStoreReceiver.
func TestManager_PropagatesStoreToRegisteredSchemes(t *testing.T) {
	mgr := auth.NewManager()
	scheme, _ := newRevokeScheme(t, nil)
	mgr.RegisterScheme("web", scheme)

	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	cookie := loginAndCookie(t, scheme, "u1")
	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected scheme to write to propagated store, got %d entries", len(list))
	}

	if err := store.Delete(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if scheme.Check(requestWith(cookie)) {
		t.Fatal("scheme must honor revocation via Manager-propagated store")
	}
}

// TestManager_RegisterSchemeAfterStoreInstalled verifies the late-registration
// path: if the store is installed before the scheme is registered, the scheme
// still receives it.
func TestManager_RegisterSchemeAfterStoreInstalled(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	scheme, _ := newRevokeScheme(t, nil)
	mgr.RegisterScheme("web", scheme)

	cookie := loginAndCookie(t, scheme, "u1")
	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("scheme registered late did not receive store: %d entries", len(list))
	}
	_ = cookie
}

// loginAndCookies performs Login with remember=true and returns both the
// session and remember cookies issued.
func loginAndCookies(t *testing.T, scheme *SessionScheme, userID string) (sess, rem *http.Cookie) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("User-Agent", "test-agent/1.0")
	r = WithSessionContext(r)
	if err := scheme.Login(w, r, &revokeTestUser{id: userID}, true); err != nil {
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
// token via the registered scheme's RememberTokenClearer impl, so the
// remember cookie alone can no longer resurrect a session on the next
// request.
func TestManager_RevokeAllSessions_ClearsRememberToken(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())
	mgr.SetServerSessionStore(store)

	scheme, userStore := newRevokeScheme(t, nil)
	mgr.RegisterScheme("web", scheme)

	_, rem := loginAndCookies(t, scheme, "u1")

	// Sanity: remember cookie alone authenticates before revocation.
	if !scheme.Check(requestWithRememberOnly(rem)) {
		t.Fatal("baseline: remember cookie must authenticate before revoke")
	}
	if userStore.users["u1"].rememberToken == "" {
		t.Fatal("baseline: remember token must be persisted on the user store")
	}

	if err := mgr.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions: %v", err)
	}

	if got := userStore.users["u1"].rememberToken; got != "" {
		t.Errorf("remember token must be cleared, got %q", got)
	}
	if scheme.Check(requestWithRememberOnly(rem)) {
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

	scheme, userStore := newRevokeScheme(t, nil)
	mgr.RegisterScheme("web", scheme)

	_, rem := loginAndCookies(t, scheme, "u1")

	list, _ := store.ListForUser(context.Background(), "u1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if err := mgr.RevokeSession(context.Background(), list[0].ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	if got := userStore.users["u1"].rememberToken; got == "" {
		t.Error("remember token must remain intact after RevokeSession")
	}
	if !scheme.Check(requestWithRememberOnly(rem)) {
		t.Error("remember cookie must still authenticate after single-session revoke")
	}
}

// TestRememberTokenClearer_UserNotFound asserts the clearer is silent
// when the user no longer exists. Manager.RevokeAllSessions calls this
// best-effort and a missing user is not an error worth surfacing.
func TestRememberTokenClearer_UserNotFound(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	if err := scheme.ClearRememberTokensForUser(context.Background(), "ghost_id"); err != nil {
		t.Fatalf("ClearRememberTokensForUser must be no-op for missing user, got %v", err)
	}
}

// noReceiverScheme is a Scheme that intentionally does NOT implement
// ServerSessionStoreReceiver or RememberTokenClearer. It pins the
// optional-interface contract: Manager must skip such schemes silently
// rather than panic during type assertion.
type noReceiverScheme struct{}

func (noReceiverScheme) Check(*http.Request) bool                { return false }
func (noReceiverScheme) User(*http.Request) auth.Authenticatable { return nil }
func (noReceiverScheme) ID(*http.Request) interface{}            { return nil }
func (noReceiverScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (noReceiverScheme) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (noReceiverScheme) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}
func (noReceiverScheme) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (noReceiverScheme) SetUserStore(auth.UserStore)                     {}

// TestManager_SchemeWithoutReceiverInterfaces_Skipped pins the type-assertion
// contract: a Scheme that implements neither ServerSessionStoreReceiver nor
// RememberTokenClearer must be silently skipped by SetServerSessionStore
// and RevokeAllSessions. Catches a regression where a future refactor
// might call methods on a non-conforming scheme and panic.
func TestManager_SchemeWithoutReceiverInterfaces_Skipped(t *testing.T) {
	mgr := auth.NewManager()
	store := session.NewMemoryStore()
	defer store.Close(context.Background())

	mgr.RegisterScheme("inert", noReceiverScheme{})

	// Setting the store must not panic and must not error.
	mgr.SetServerSessionStore(store)

	// RevokeAllSessions must succeed (store delete only, clearer skipped).
	if err := mgr.RevokeAllSessions(context.Background(), "u1"); err != nil {
		t.Fatalf("RevokeAllSessions must not error for non-receiver scheme, got %v", err)
	}
}

// failingClearerScheme wraps SessionScheme but overrides
// ClearRememberTokensForUser to always return an error, exercising the
// new ErrRememberClearPartial return path.
type failingClearerScheme struct {
	*SessionScheme
	err error
}

func (f *failingClearerScheme) ClearRememberTokensForUser(context.Context, string) error {
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

	innerScheme, _ := newRevokeScheme(t, nil)
	clearerErr := errors.New("user store DB down")
	mgr.RegisterScheme("web", &failingClearerScheme{SessionScheme: innerScheme, err: clearerErr})

	// Seed a session so DeleteAllForUser has something to delete.
	_, _ = loginAndCookies(t, innerScheme, "u1")

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

// Ctx-suffixed shims for auth.UserStore, added in Sweep 1b.
func (p *revokeTestStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *revokeTestStore) FindByCredentialsCtx(_ context.Context, credentials map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(credentials)
}
func (p *revokeTestStore) UpdateRememberTokenCtx(_ context.Context, user auth.Authenticatable, token string) error {
	return p.UpdateRememberToken(user, token)
}
