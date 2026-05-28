package guards

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/router"
)

// trackingSession wraps an *auth.BaseSession but counts Save() calls so the
// save-at-end middleware test can prove the persistence path actually fires.
type trackingSession struct {
	*auth.BaseSession
	saves     int32
	saveError error
}

func newTrackingSession() *trackingSession {
	return &trackingSession{BaseSession: auth.NewSession("track-session")}
}

func (s *trackingSession) Save(w http.ResponseWriter) error {
	atomic.AddInt32(&s.saves, 1)
	return s.saveError
}

// trackingStore returns the same trackingSession for every Get so the
// middleware-installed holder picks it up via getSession.
type trackingStore struct {
	session *trackingSession
}

func (s *trackingStore) Create(id string) (auth.Session, error) {
	return s.session, nil
}

func (s *trackingStore) Get(r *http.Request, id string) (auth.Session, error) {
	return s.session, nil
}

func (s *trackingStore) Save(w http.ResponseWriter, session auth.Session) error {
	return nil
}

func (s *trackingStore) Destroy(id string) error                        { return nil }
func (s *trackingStore) GarbageCollect(maxLifetime time.Duration) error { return nil }

// helper: build a SessionGuard whose getSession returns the trackingSession.
func newGuardForMiddleware(t *testing.T, store *trackingStore) *SessionGuard {
	t.Helper()
	g := func() *SessionGuard {
		g := &SessionGuard{
			store:  store,
			config: auth.SessionConfig{Name: "test_session"},
			hasher: auth.NewBcryptHasher(4),
		}
		g.provider.Store(&providerHolder{p: &mockSessionGuardUserProvider{}})
		g.throttler.Store(&throttlerHolder{t: auth.NoopLoginThrottler{}})
		return g
	}()
	return g
}

// runMiddleware drives a single request through SessionMiddleware so we can
// assert the post-handler save fired (or did not).
func runMiddleware(t *testing.T, g *SessionGuard, mutate func(s auth.Session)) (*trackingSession, *httptest.ResponseRecorder) {
	t.Helper()

	store, ok := g.store.(*trackingStore)
	if !ok {
		t.Fatalf("expected trackingStore, got %T", g.store)
	}

	mw := g.SessionMiddleware()
	handler := mw(func(c *router.Context) error {
		// Resolve the session through the guard so the holder cache
		// is populated exactly the way real request paths do.
		sess := g.getSession(c.Request)
		if sess == nil {
			t.Fatalf("getSession returned nil inside handler")
		}
		if mutate != nil {
			mutate(sess)
		}
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := router.NewContext(rec, req)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	return store.session, rec
}

func TestSessionMiddleware_PersistsModifiedSessionAfterHandler(t *testing.T) {
	store := &trackingStore{session: newTrackingSession()}
	g := newGuardForMiddleware(t, store)

	sess, _ := runMiddleware(t, g, func(s auth.Session) {
		s.Put("user_id", "u-1")
	})

	if got := atomic.LoadInt32(&sess.saves); got != 1 {
		t.Fatalf("expected exactly one Save() call after handler, got %d", got)
	}
}

func TestSessionMiddleware_NoopWhenSessionUnmodified(t *testing.T) {
	store := &trackingStore{session: newTrackingSession()}
	g := newGuardForMiddleware(t, store)

	sess, _ := runMiddleware(t, g, func(s auth.Session) {
		// Read-only: Get returns nil, no Put, no Flash. The session
		// should remain pristine; the middleware MUST NOT call Save.
		_ = s.Get("user_id")
	})

	if got := atomic.LoadInt32(&sess.saves); got != 0 {
		t.Fatalf("expected no Save() call on a read-only request, got %d", got)
	}
}

func TestSessionMiddleware_NoopWhenSessionNeverAccessed(t *testing.T) {
	store := &trackingStore{session: newTrackingSession()}
	g := newGuardForMiddleware(t, store)

	mw := g.SessionMiddleware()
	handler := mw(func(c *router.Context) error {
		// Handler never touches the session at all (the dominant
		// request path on a typical site). The holder is attached
		// but holder.session stays nil; Save MUST NOT fire.
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := router.NewContext(rec, req)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if got := atomic.LoadInt32(&store.session.saves); got != 0 {
		t.Fatalf("expected no Save() call when handler never resolved a session, got %d", got)
	}
}

// TestSessionMiddleware_EagerlyBootstrapsForAnonymousVisitor pins the
// eager-bootstrap fix. SessionMiddleware MUST resolve (loading or
// creating) a session for every request even when the handler never
// touches it, so an anonymous visitor's first response carries a
// Set-Cookie. Without this, the CSRF middleware's safe-method
// XSRF-TOKEN bootstrap has no session id to bind to and the very
// next POST 419's with no useful token state.
//
// The test fixture uses a freshly minted session (auth.NewSession("")
// → modified=true) so the post-handler doSave path actually fires.
// The pre-fix middleware skipped getSession entirely when no handler
// called it, so the trackingStore never observed a Save() call.
func TestSessionMiddleware_EagerlyBootstrapsForAnonymousVisitor(t *testing.T) {
	store := &trackingStore{session: &trackingSession{BaseSession: auth.NewSession("")}}
	g := newGuardForMiddleware(t, store)

	mw := g.SessionMiddleware()
	handler := mw(func(c *router.Context) error {
		// Handler never touches the session. Eager bootstrap must
		// still ensure Save fires because store.Create("") returned a
		// freshly minted, modified session.
		return nil
	})

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	c := router.NewContext(rec, req)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if got := atomic.LoadInt32(&store.session.saves); got != 1 {
		t.Fatalf("expected eager bootstrap to call Save() once for anonymous visitor, got %d", got)
	}
}

func TestSessionMiddleware_FlashWriteIsPersisted(t *testing.T) {
	// Flash() mutates the bag and marks modified. The middleware MUST
	// pick that up the same as a Put. Laravel-equivalent flash messages
	// rely on this round-trip.
	store := &trackingStore{session: newTrackingSession()}
	g := newGuardForMiddleware(t, store)

	sess, _ := runMiddleware(t, g, func(s auth.Session) {
		s.Flash("status", "saved")
	})

	if got := atomic.LoadInt32(&sess.saves); got != 1 {
		t.Fatalf("expected Save() to fire after Flash() write, got %d", got)
	}
}

func TestSessionMiddleware_DestroyedSessionIsSaved(t *testing.T) {
	// Logout calls Invalidate() then Save() inline already, but if any
	// handler invalidates the session and forgets to Save, the middleware
	// MUST still flush so the cookie-delete header is emitted.
	store := &trackingStore{session: newTrackingSession()}
	g := newGuardForMiddleware(t, store)

	sess, _ := runMiddleware(t, g, func(s auth.Session) {
		_ = s.Invalidate()
	})

	if got := atomic.LoadInt32(&sess.saves); got < 1 {
		t.Fatalf("expected Save() to fire on a destroyed session, got %d", got)
	}
}
