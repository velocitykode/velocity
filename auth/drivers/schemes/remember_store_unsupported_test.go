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

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
)

// casSessionSchemeUserStore adds the remember-token compare-and-swap to the
// mock user store, for the cases that sign in with remember-me.
type casSessionSchemeUserStore struct {
	*mockSessionSchemeUserStore
}

func (casSessionSchemeUserStore) CompareAndSwapRememberToken(context.Context, contract.Authenticatable, string, string) (bool, error) {
	return true, nil
}

// plainRememberStore is a user store with no compare-and-swap. It counts
// every lookup and every remember-token write it is asked for.
type plainRememberStore struct {
	mu      sync.Mutex
	user    *revokeTestUser
	lookups atomic.Int32
	writes  atomic.Int32
}

func (p *plainRememberStore) FindByID(interface{}) (contract.Authenticatable, error) {
	p.lookups.Add(1)
	return p.user, nil
}
func (p *plainRememberStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *plainRememberStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *plainRememberStore) FindByCredentialsCtx(_ context.Context, c map[string]interface{}) (contract.Authenticatable, error) {
	return p.FindByCredentials(c)
}
func (p *plainRememberStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *plainRememberStore) UpdateRememberToken(_ contract.Authenticatable, token string) error {
	p.writes.Add(1)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.user.rememberToken = token
	return nil
}
func (p *plainRememberStore) UpdateRememberTokenCtx(_ context.Context, u contract.Authenticatable, token string) error {
	return p.UpdateRememberToken(u, token)
}

func (p *plainRememberStore) token() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.user.rememberToken
}

// requestSessionUserID returns the user id the request's session holds.
func requestSessionUserID(r *http.Request) any {
	holder, ok := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	if !ok || holder == nil {
		return nil
	}
	sess := holder.getSession()
	if sess == nil {
		return nil
	}
	return sess.Get(auth.UserIDSessionKey)
}

// A scheme whose user store cannot consume a remember credential by
// compare-and-swap never issues one: Login, LoginByID and Attempt with
// remember-me return auth.ErrRememberTokenStoreUnsupported before anything
// changes, and the same sign-in without remember-me works.
func TestSessionScheme_RememberNeedsTheCompareAndSwapStore(t *testing.T) {
	signIns := []struct {
		name string
		do   func(g *SessionScheme, w http.ResponseWriter, r *http.Request, remember ...bool) error
	}{
		{"Login", func(g *SessionScheme, w http.ResponseWriter, r *http.Request, remember ...bool) error {
			return g.Login(w, r, &revokeTestUser{id: "u1"}, remember...)
		}},
		{"LoginByID", func(g *SessionScheme, w http.ResponseWriter, r *http.Request, remember ...bool) error {
			return g.LoginByID(w, r, "u1", remember...)
		}},
		{"Attempt", func(g *SessionScheme, w http.ResponseWriter, r *http.Request, remember ...bool) error {
			_, err := g.Attempt(w, r, map[string]interface{}{"email": "u1", "password": "pw"}, remember...)
			return err
		}},
	}
	for _, tt := range signIns {
		t.Run(tt.name, func(t *testing.T) {
			scheme, _ := newRevokeScheme(t, nil)
			scheme.SetAttemptFloor(-1)
			users := &plainRememberStore{user: &revokeTestUser{id: "u1"}}
			scheme.SetUserStore(users)

			w := httptest.NewRecorder()
			r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
			err := tt.do(scheme, w, r, true)
			if !errors.Is(err, auth.ErrRememberTokenStoreUnsupported) {
				t.Fatalf("%s with remember-me = %v, want auth.ErrRememberTokenStoreUnsupported", tt.name, err)
			}
			if uid := requestSessionUserID(r); uid != nil {
				t.Errorf("the refused sign-in anchored user %v", uid)
			}
			if users.writes.Load() != 0 || users.token() != "" {
				t.Errorf("the refused sign-in wrote a remember token (%d writes)", users.writes.Load())
			}
			for _, c := range w.Result().Cookies() {
				if c.Value != "" {
					t.Errorf("the refused sign-in set cookie %s", c.Name)
				}
			}

			w = httptest.NewRecorder()
			r = WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
			if err := tt.do(scheme, w, r); err != nil {
				t.Fatalf("%s without remember-me: %v", tt.name, err)
			}
			if uid := requestSessionUserID(r); uid != "u1" {
				t.Errorf("sign-in without remember-me anchored %v, want u1", uid)
			}
		})
	}
}

// A typed-nil user store has no capability either: the sign-in is refused
// with the sentinel instead of reaching a nil store.
func TestSessionScheme_RememberWithATypedNilUserStore(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	scheme.userStore.Store(&userStoreHolder{p: (*revokeTestStore)(nil)})

	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err := scheme.Login(w, r, &revokeTestUser{id: "u1"}, true); !errors.Is(err, auth.ErrRememberTokenStoreUnsupported) {
		t.Fatalf("Login with remember-me on a typed-nil user store = %v, want auth.ErrRememberTokenStoreUnsupported", err)
	}
	if _, ok := scheme.rememberStore(); ok {
		t.Fatal("a typed-nil user store reported the compare-and-swap capability")
	}
}

// Rotation reports the same sentinel when the store lost the capability.
func TestRotateRememberToken_StoreWithoutCompareAndSwap(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	scheme.SetUserStore(&plainRememberStore{user: &revokeTestUser{id: "u1"}})
	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))
	r.Context().Value(sessionCtxKey{}).(*sessionHolder).setResponseWriter(w)
	var op gateOp
	if err := scheme.rotateRememberToken(r, rememberMatch{user: &revokeTestUser{id: "u1"}}, &op); !errors.Is(err, auth.ErrRememberTokenStoreUnsupported) {
		t.Fatalf("rotateRememberToken = %v, want auth.ErrRememberTokenStoreUnsupported", err)
	}
}

// A remember cookie presented to a scheme without the capability is
// deleted on the response and ignored, without asking the user store, and
// the scheme says so once however many requests present one at once.
func TestSessionScheme_PresentedRememberCookieOnAStoreWithoutCompareAndSwap(t *testing.T) {
	const requests = 16
	scheme, _ := newRevokeScheme(t, nil)
	base := &rememberRevivalStore{user: &revokeTestUser{id: "u1"}}
	scheme.SetUserStore(base)
	cookie := mintRememberCookie(t, scheme)
	issued := base.user.rememberToken

	users := &plainRememberStore{user: &revokeTestUser{id: "u1", rememberToken: issued}}
	scheme.SetUserStore(users)
	logs := &kvLog{}
	scheme.SetLogger(logs)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range requests {
		wg.Go(func() {
			<-start
			w := httptest.NewRecorder()
			r := rememberRecallRequest(t, cookie, w)
			if u := scheme.User(r); u != nil {
				t.Errorf("a remember cookie signed in %v on a scheme without remember-me", u.GetAuthIdentifier())
			}
			if c := findRememberCookie(w); c == nil || c.Value != "" || c.MaxAge >= 0 {
				t.Errorf("remember cookie on the response = %+v, want its deletion", c)
			}
		})
	}
	close(start)
	wg.Wait()

	if n := users.lookups.Load(); n != 0 {
		t.Errorf("the user store was asked %d time(s) about a cookie the scheme cannot honour", n)
	}
	if users.writes.Load() != 0 || users.token() != issued {
		t.Errorf("the stored remember token was written (%d writes)", users.writes.Load())
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	warned := 0
	for _, e := range logs.entries {
		if strings.Contains(e.msg, "RememberTokenCompareAndSwapper") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("the unsupported remember cookie was logged %d times, want once per scheme", warned)
	}
}

// A revoked session's remember credential is ended by compare-and-swap
// only: a store without it is never asked for the unconditional clear,
// which could erase a credential issued since the cookie matched.
func TestBurnPresentedRememberToken_NeverClearsUnconditionally(t *testing.T) {
	records := session.NewMemoryStore()
	t.Cleanup(func() { _ = records.Close(context.Background()) })
	scheme, _ := newRevokeScheme(t, records)
	sess, rem := loginAndCookies(t, scheme, "u1")
	user, err := scheme.loadUserStore().FindByIDCtx(context.Background(), "u1")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	issued := user.GetRememberToken()

	// The store loses the capability, then the session is revoked.
	users := &plainRememberStore{user: &revokeTestUser{id: "u1", rememberToken: issued}}
	scheme.SetUserStore(users)
	list, err := records.ListForUser(context.Background(), "u1")
	if err != nil || len(list) != 1 {
		t.Fatalf("expected 1 stored session, got %d err=%v", len(list), err)
	}
	if err := records.Delete(context.Background(), list[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(sess)
	r.AddCookie(rem)
	r = WithSessionContext(r)
	r.Context().Value(sessionCtxKey{}).(*sessionHolder).setResponseWriter(w)
	if ok, err := scheme.CheckWithError(r); ok || !errors.Is(err, auth.ErrSessionRevoked) {
		t.Fatalf("CheckWithError = %v, %v; want false and ErrSessionRevoked", ok, err)
	}
	if n := users.writes.Load(); n != 0 || users.token() != issued {
		t.Fatalf("the revoked request cleared the stored token unconditionally (%d writes, token %q)", n, users.token())
	}
	if c := findRememberCookie(w); c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Errorf("remember cookie on the response = %+v, want its deletion", c)
	}
}

// The rotation swaps from the hash the cookie validated against, as it was
// read then. A user store that hands every request the same user value
// shows a later hash on it once another recall has rotated: reading the
// token from the user again would swap from that later hash and let one
// presented credential be consumed twice.
func TestRotateRememberToken_SwapsFromTheValidatedHash(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	users := &rememberRevivalStore{user: &revokeTestUser{id: "u1"}}
	scheme.SetUserStore(users)
	cookie := mintRememberCookie(t, scheme)
	issued := users.user.rememberToken

	// The first request validates the cookie and stops there.
	first := rememberRecallRequest(t, cookie, httptest.NewRecorder())
	match, ok := scheme.matchRememberCookie(first)
	if !ok || match.hash != issued {
		t.Fatalf("matchRememberCookie = %+v, %v; want the issued hash", match, ok)
	}

	// A second request presents the same cookie and completes its recall:
	// the shared user value now carries the replacement hash.
	if u := recallThroughSeam(t, scheme, cookie, httptest.NewRecorder()); u == nil {
		t.Fatal("the second request's recall failed")
	}
	rotated := users.user.rememberToken
	if rotated == issued || match.user.GetRememberToken() != rotated {
		t.Fatalf("fixture: the shared user value does not show the rotated hash (%q -> %q)", issued, rotated)
	}

	// The first request's rotation must lose: its credential is consumed.
	var op gateOp
	if err := scheme.rotateRememberToken(first, match, &op); !errors.Is(err, errRememberTokenStale) {
		t.Fatalf("rotateRememberToken on a consumed credential = %v, want errRememberTokenStale", err)
	}
	if users.user.rememberToken != rotated {
		t.Fatal("the losing rotation replaced the winner's credential")
	}
}

// detachedTokenStore persists the remember token in its own field and
// never touches the user value it hands out: whatever shows on that value
// was put there by someone else.
type detachedTokenStore struct {
	mu     sync.Mutex
	user   *revokeTestUser
	stored string
}

func (p *detachedTokenStore) FindByID(interface{}) (contract.Authenticatable, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// A fresh value per lookup, carrying the stored token, as a database
	// read would; the caller's writes to it reach nothing.
	return &revokeTestUser{id: p.user.id, rememberToken: p.stored}, nil
}
func (p *detachedTokenStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *detachedTokenStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return nil, errors.New("unused")
}
func (p *detachedTokenStore) FindByCredentialsCtx(_ context.Context, c map[string]interface{}) (contract.Authenticatable, error) {
	return p.FindByCredentials(c)
}
func (p *detachedTokenStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *detachedTokenStore) UpdateRememberToken(_ contract.Authenticatable, token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stored = token
	return nil
}
func (p *detachedTokenStore) UpdateRememberTokenCtx(_ context.Context, u contract.Authenticatable, token string) error {
	return p.UpdateRememberToken(u, token)
}
func (p *detachedTokenStore) CompareAndSwapRememberToken(_ context.Context, _ contract.Authenticatable, oldToken, newToken string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stored != oldToken {
		return false, nil
	}
	p.stored = newToken
	return true, nil
}

// The scheme writes a remember token through the store only. It never
// sets it on the user value: on a store that shares one user value across
// requests, a set after the swap returned would land over whatever swapped
// in or cleared the credential since. Issue, rotation and the rotation's
// undo all leave the user value as the store left it.
func TestSessionScheme_NeverSetsTheRememberTokenOnTheUserValue(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	users := &detachedTokenStore{user: &revokeTestUser{id: "u1"}}
	scheme.SetUserStore(users)

	// Issue at sign-in.
	signedIn := &revokeTestUser{id: "u1", rememberToken: "as the store left it"}
	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err := scheme.Login(w, r, signedIn, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	cookie := findRememberCookie(w)
	if cookie == nil || users.stored == "" {
		t.Fatalf("no remember credential issued (cookie %v, stored %q)", cookie, users.stored)
	}
	if signedIn.rememberToken != "as the store left it" {
		t.Fatalf("the sign-in set the token on the user value: %q", signedIn.rememberToken)
	}

	// Rotation on recall.
	issued := users.stored
	first := rememberRecallRequest(t, cookie, httptest.NewRecorder())
	match, ok := scheme.matchRememberCookie(first)
	if !ok {
		t.Fatal("the issued cookie did not validate")
	}
	var op gateOp
	if _, _, err := reserveOperation(first, &op); err != nil {
		t.Fatalf("reserveOperation: %v", err)
	}
	if err := scheme.rotateRememberToken(first, match, &op); err != nil {
		t.Fatalf("rotateRememberToken: %v", err)
	}
	if users.stored == issued {
		t.Fatal("the rotation did not swap the stored token")
	}
	if got := match.user.GetRememberToken(); got != issued {
		t.Fatalf("the rotation set the token on the user value: %q, want it as the store left it (%q)", got, issued)
	}

	// The undo of a rotation whose session was not saved restores the
	// stored token and leaves the user value alone too.
	match.user.SetRememberToken("changed since by the store")
	op.abort()
	if users.stored != issued {
		t.Fatalf("the undo did not restore the stored token: %q, want %q", users.stored, issued)
	}
	if got := match.user.GetRememberToken(); got != "changed since by the store" {
		t.Fatalf("the undo set the token on the user value: %q", got)
	}
}

// swapUsersOnPut installs another user store on the scheme when the
// sign-in writes its session record: after the sign-in checked its store
// for the remember capability, before the credential is issued.
type swapUsersOnPut struct {
	*session.MemoryStore
	once sync.Once
	swap func()
}

func (s *swapUsersOnPut) Put(ctx context.Context, rec *auth.StoredSession) error {
	s.once.Do(s.swap)
	return s.MemoryStore.Put(ctx, rec)
}

// The store a sign-in checked for the remember capability is the store its
// credential is issued through: a SetUserStore in between does not get a
// credential written through a store that was never checked.
func TestSessionScheme_RememberIsIssuedThroughTheStoreThatWasChecked(t *testing.T) {
	inner := session.NewMemoryStore()
	t.Cleanup(func() { _ = inner.Close(context.Background()) })
	records := &swapUsersOnPut{MemoryStore: inner}
	scheme, checked := newRevokeScheme(t, records)
	plain := &plainRememberStore{user: &revokeTestUser{id: "u1"}}
	records.swap = func() { scheme.SetUserStore(plain) }

	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err := scheme.Login(w, r, &revokeTestUser{id: "u1"}, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if scheme.loadUserStore() != auth.UserStore(plain) {
		t.Fatal("fixture: the user store was not swapped during the sign-in")
	}
	if n := plain.writes.Load(); n != 0 {
		t.Fatalf("the credential was issued through the store installed after the check (%d writes)", n)
	}
	if findRememberCookie(w) == nil || checked.token("u1") == "" {
		t.Fatalf("no credential issued through the checked store (token %q)", checked.token("u1"))
	}
}

// A remember cookie the scheme cannot honour is deleted on every request
// that presents it, also one whose session is signed in and never reaches
// a recall, with one deletion line on the response, and logged once.
func TestSessionScheme_UnsupportedRememberCookieIsDroppedOnASignedInRequest(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	sess, rem := loginAndCookies(t, scheme, "u1")

	users := &plainRememberStore{user: &revokeTestUser{id: "u1"}}
	scheme.SetUserStore(users)
	logs := &kvLog{}
	scheme.SetLogger(logs)

	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(sess)
		r.AddCookie(rem)
		r = WithSessionContext(r)
		r.Context().Value(sessionCtxKey{}).(*sessionHolder).setResponseWriter(w)
		if !scheme.Check(r) {
			t.Fatal("the signed-in session was refused")
		}
		deletions := 0
		for _, c := range w.Result().Cookies() {
			if c.Name == "remember_vel_session" {
				if c.Value != "" || c.MaxAge >= 0 {
					t.Fatalf("remember cookie on the response = %+v, want its deletion", c)
				}
				deletions++
			}
		}
		if deletions != 1 {
			t.Fatalf("%d remember-cookie deletions on a signed-in request, want exactly 1", deletions)
		}
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	warned := 0
	for _, e := range logs.entries {
		if strings.Contains(e.msg, "RememberTokenCompareAndSwapper") {
			warned++
		}
	}
	if warned != 1 {
		t.Fatalf("the unsupported remember cookie was logged %d times over two requests, want once per scheme", warned)
	}
}
