package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// hookSession is a custom auth.Session whose mutators run a hook, so a
// test can make one change the session and then panic. It does not report
// IsModified, so the commit saves it unconditionally and saves counts
// every save.
type hookSession struct {
	*mockSession
	onRegenerate func(s *hookSession)
	onInvalidate func(s *hookSession)
	onRemove     func(s *hookSession, key string)
	saves        atomic.Int32
}

func newHookSession() *hookSession {
	return &hookSession{mockSession: newMockSession()}
}

func (s *hookSession) Regenerate() error {
	if s.onRegenerate != nil {
		s.onRegenerate(s)
	}
	return nil
}

func (s *hookSession) Invalidate() error {
	if s.onInvalidate != nil {
		s.onInvalidate(s)
	}
	return s.mockSession.Invalidate()
}

func (s *hookSession) Remove(key string) {
	if s.onRemove != nil {
		s.onRemove(s, key)
		return
	}
	s.mockSession.Remove(key)
}

func (s *hookSession) Save(http.ResponseWriter) error {
	s.saves.Add(1)
	return nil
}

// hookSessionStore hands out one session for every load and create.
type hookSessionStore struct{ s auth.Session }

func (st *hookSessionStore) Create(string) (auth.Session, error)             { return st.s, nil }
func (st *hookSessionStore) Get(*http.Request, string) (auth.Session, error) { return st.s, nil }
func (st *hookSessionStore) Save(w http.ResponseWriter, s auth.Session) error {
	return s.Save(w)
}
func (st *hookSessionStore) Destroy(string) error                           { return nil }
func (st *hookSessionStore) GarbageCollect(maxLifetime time.Duration) error { return nil }

// panicRevokeRotator is a CSRF token rotator whose RevokeToken removes the
// token from the session it is handed (as the framework's store, which
// keeps the token in the session, does) and then panics.
type panicRevokeRotator struct{}

func (panicRevokeRotator) RotateToken(context.Context, string, string) error { return nil }
func (panicRevokeRotator) RevokeToken(ctx context.Context, _ string) error {
	if s := SessionFromContext(ctx); s != nil {
		s.Remove("_csrf")
	}
	panic("revoke panicked after changing the session")
}
func (panicRevokeRotator) WriteXSRFCookie(context.Context, http.ResponseWriter, string) {}
func (panicRevokeRotator) ClearXSRFCookie(http.ResponseWriter, *http.Request)           {}

// expiringServerStore reports every record expired.
type expiringServerStore struct{ holderRaceStore }

func (*expiringServerStore) Get(context.Context, string) (*auth.StoredSession, error) {
	return nil, auth.ErrSessionExpired
}

// newHookScheme returns a session scheme whose store hands out s, with the
// revoke-suite user store (users u1 and u2) and a real encryptor, so a
// remember cookie can be minted for a recall.
func newHookScheme(t *testing.T, s auth.Session) (*SessionScheme, *revokeTestStore) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}, "u2": {id: "u2"}}}
	g, err := NewSessionScheme(users, auth.SessionConfig{Name: "vel_session", Path: "/", HttpOnly: true}, enc, WithSessionStore(&hookSessionStore{s: s}))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	return g, users
}

// seamRequest returns a request served inside the session save seam, as
// SessionMiddleware binds it, with s as the request's session.
func seamRequest(s auth.Session) (*http.Request, *httptest.ResponseRecorder, *sessionHolder) {
	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/", nil))
	h := r.Context().Value(sessionCtxKey{}).(*sessionHolder)
	h.setResponseWriter(w)
	h.markSaveScope()
	h.setSession(s)
	return r, w, h
}

// addRememberCookie mints a remember credential for user u1 and presents it
// on r.
func addRememberCookie(t *testing.T, g *SessionScheme, users *revokeTestStore, r *http.Request) {
	t.Helper()
	u := users.users["u1"]
	c, err := g.mintRememberCookie(u, func(hashed string) error { return users.UpdateRememberToken(u, hashed) })
	if err != nil {
		t.Fatalf("mint remember cookie: %v", err)
	}
	r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
}

// mustPanic runs fn and fails the test when it returns without panicking.
func mustPanic(t *testing.T, fn func()) {
	t.Helper()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		fn()
	}()
	if !panicked {
		t.Fatal("premise: the operation did not panic")
	}
}

// An authentication operation that changed the session and then panicked,
// in whichever session mutator or session-bound hook, leaves the request
// torn: the commit saves nothing and the request reads as signed out, so
// no half-transitioned session is persisted. The session is changed before
// the panic in every case, including before the operation counted a
// transition.
func TestSessionScheme_MutatorPanicAfterChangeTearsTheRequest(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, g *SessionScheme, users *revokeTestStore, s *hookSession, w http.ResponseWriter, r *http.Request)
	}{
		{"Login Regenerate", func(t *testing.T, g *SessionScheme, _ *revokeTestStore, s *hookSession, w http.ResponseWriter, r *http.Request) {
			s.onRegenerate = func(s *hookSession) { s.id = "half-regenerated"; panic("regenerate") }
			mustPanic(t, func() { _ = g.Login(w, r, &revokeTestUser{id: "u2"}) })
		}},
		{"Logout Invalidate", func(t *testing.T, g *SessionScheme, _ *revokeTestStore, s *hookSession, w http.ResponseWriter, r *http.Request) {
			s.onInvalidate = func(s *hookSession) { delete(s.data, auth.UserIDSessionKey); panic("invalidate") }
			mustPanic(t, func() { _ = g.Logout(w, r) })
		}},
		{"Logout CSRF revoke", func(t *testing.T, g *SessionScheme, _ *revokeTestStore, _ *hookSession, w http.ResponseWriter, r *http.Request) {
			g.SetCSRFTokenRotator(panicRevokeRotator{})
			mustPanic(t, func() { _ = g.Logout(w, r) })
		}},
		{"recall Regenerate", func(t *testing.T, g *SessionScheme, users *revokeTestStore, s *hookSession, _ http.ResponseWriter, r *http.Request) {
			delete(s.data, auth.UserIDSessionKey)
			addRememberCookie(t, g, users, r)
			s.onRegenerate = func(s *hookSession) { s.id = "half-regenerated"; panic("regenerate") }
			mustPanic(t, func() { _ = g.User(r) })
		}},
		{"recall after expired record Remove", func(t *testing.T, g *SessionScheme, users *revokeTestStore, s *hookSession, _ http.ResponseWriter, r *http.Request) {
			g.SetServerSessionStore(&expiringServerStore{})
			addRememberCookie(t, g, users, r)
			s.onRemove = func(s *hookSession, key string) { delete(s.data, key); panic("remove") }
			mustPanic(t, func() { _ = g.User(r) })
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newHookSession()
			s.data[auth.UserIDSessionKey] = "u1"
			s.data["_csrf"] = "token"
			g, users := newHookScheme(t, s)
			r, w, h := seamRequest(s)
			tc.run(t, g, users, s, w, r)
			s.onRegenerate, s.onInvalidate, s.onRemove = nil, nil, nil

			if u := g.User(r); u != nil {
				t.Errorf("a read after the torn operation returned user %v, want signed out", u.GetAuthIdentifier())
			}
			err := commitSession(g, r, w, h)
			if !errors.Is(err, errOperationTorn) {
				t.Errorf("commit after the torn operation returned %v, want errOperationTorn", err)
			}
			if n := s.saves.Load(); n != 0 {
				t.Errorf("commit saved the half-transitioned session %d time(s), want none", n)
			}
		})
	}
}

// sealCountingSession is a hookSession that counts Seal calls.
type sealCountingSession struct {
	*hookSession
	seals atomic.Int32
}

func (s *sealCountingSession) Seal() { s.seals.Add(1) }

// The commit changes the session (Seal) only once it holds the request's
// reservation: a commit refused because an operation holds it leaves the
// session alone, so a custom session is never changed by the commit while
// the operation changes it.
func TestCommitSession_RefusedCommitDoesNotSealTheSession(t *testing.T) {
	s := &sealCountingSession{hookSession: newHookSession()}
	g, _ := newHookScheme(t, s)
	r, w, h := seamRequest(s)
	var op gateOp
	if err := h.reserve(&op); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	defer op.release()

	if err := commitSession(g, r, w, h); !errors.Is(err, auth.ErrOperationInProgress) {
		t.Fatalf("commit while an operation holds the gate returned %v, want auth.ErrOperationInProgress", err)
	}
	if n := s.seals.Load(); n != 0 {
		t.Errorf("refused commit sealed the session %d time(s), want none", n)
	}
}

// A commit that holds the reservation seals the session before it saves.
func TestCommitSession_SealsTheSessionItSaves(t *testing.T) {
	s := &sealCountingSession{hookSession: newHookSession()}
	g, _ := newHookScheme(t, s)
	r, w, h := seamRequest(s)
	if err := commitSession(g, r, w, h); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := s.seals.Load(); n != 1 {
		t.Errorf("commit sealed the session %d time(s), want 1", n)
	}
	if n := s.saves.Load(); n != 1 {
		t.Errorf("commit saved the session %d time(s), want 1", n)
	}
}

// A deletion of the session cookie a queued write adds ends the session
// the commit issued under the request's reservation: the session is
// invalidated while the commit holds the gate, not after it freed it.
// When another operation of the request holds the gate by then, the
// session object is left to it and only the issued id is retired (revoked
// in the cookie store, its server record deleted).
func TestCommitSession_DeletionAfterSaveEndsTheSessionUnderTheReservation(t *testing.T) {
	for _, busy := range []bool{false, true} {
		name := "gate free"
		if busy {
			name = "gate held by another operation"
		}
		t.Run(name, func(t *testing.T) {
			s := newHookSession()
			s.data[auth.UserIDSessionKey] = "u1"
			g, _ := newHookScheme(t, s)
			var deletes atomic.Int32
			records := &deleteHookServerStore{holderRaceStore: holderRaceStore{user: "u1"}}
			records.onDelete = func() { deletes.Add(1) }
			g.SetServerSessionStore(records)
			r, w, h := seamRequest(s)

			var (
				invalidated     atomic.Int32
				invalidatedFree atomic.Bool
			)
			s.onInvalidate = func(*hookSession) {
				invalidated.Add(1)
				h.mu.RLock()
				if !h.busy {
					invalidatedFree.Store(true)
				}
				h.mu.RUnlock()
			}
			var other gateOp
			if !QueueAfterSessionSave(r, func(w http.ResponseWriter) {
				http.SetCookie(w, &http.Cookie{Name: "vel_session", Value: "", MaxAge: -1})
				if busy {
					if err := h.reserve(&other); err != nil {
						t.Errorf("premise: reserve after the commit freed the gate: %v", err)
					}
				}
			}) {
				t.Fatal("premise: write not queued")
			}
			if err := commitSession(g, r, w, h); err != nil {
				t.Fatalf("commit: %v", err)
			}
			h.mu.RLock()
			stillHeld := h.busy
			h.mu.RUnlock()
			if busy && !stillHeld {
				t.Error("the commit freed the gate another operation holds")
			}
			other.release()

			if busy {
				if n := invalidated.Load(); n != 0 {
					t.Errorf("the session was invalidated %d time(s) while another operation held the gate, want none", n)
				}
			} else {
				if n := invalidated.Load(); n != 1 {
					t.Errorf("the session was invalidated %d time(s), want 1", n)
				}
				if invalidatedFree.Load() {
					t.Error("the session was invalidated with the gate free, outside the reservation")
				}
			}
			if n := deletes.Load(); n != 1 {
				t.Errorf("the issued session's record was deleted %d time(s), want 1", n)
			}
		})
	}
}

// freshSessionStore hands out a new session on every load, after both of
// two concurrent loads entered it.
type freshSessionStore struct {
	hookSessionStore
	arrived chan struct{}
	both    chan struct{}
	n       atomic.Int32
}

func (st *freshSessionStore) Get(*http.Request, string) (auth.Session, error) {
	st.arrived <- struct{}{}
	<-st.both
	s := newHookSession()
	s.id = "load-" + string(rune('0'+st.n.Add(1)))
	return s, nil
}

// Two goroutines of one request that load its session at the same time
// get the same session object: the first load cached on the request wins
// and the other load's session is dropped, so no operation changes a
// session the request no longer holds.
func TestSessionScheme_ConcurrentFirstLoadsShareOneSession(t *testing.T) {
	st := &freshSessionStore{arrived: make(chan struct{}), both: make(chan struct{})}
	g, err := NewSessionScheme(&revokeTestStore{users: map[string]*revokeTestUser{}}, auth.SessionConfig{Name: "vel_session"}, nil, WithSessionStore(st))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	r := WithSessionContext(httptest.NewRequest(http.MethodGet, "/", nil))
	r.AddCookie(&http.Cookie{Name: "vel_session", Value: "presented"})
	got := make(chan auth.Session, 2)
	for range 2 {
		go func() { got <- g.Session(r) }()
	}
	<-st.arrived
	<-st.arrived
	close(st.both)
	a, b := <-got, <-got
	if a == nil || a != b {
		t.Fatalf("concurrent first loads returned different sessions: %v and %v", a, b)
	}
	if cached := sessionFromHolder(r); cached != a {
		t.Fatalf("the request caches %v, not the session both loads returned", cached)
	}
}
