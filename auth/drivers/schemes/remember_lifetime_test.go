package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/router"
)

const (
	sessionCookieName  = "vel_session"
	rememberCookieName = "remember_vel_session"
	// defaultRememberSeconds is the remember lifetime a SessionConfig
	// with RememberLifetime unset gives the remember cookie: 30 days.
	defaultRememberSeconds = 30 * 24 * 60 * 60
)

// rememberBrowser drives a scheme through the session save seam the way a
// browser does, including dropping a cookie once its Max-Age has run out
// on the session clock, so a test sees exactly the cookies a real browser
// would still send after hours of idleness.
type rememberBrowser struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
	expires map[string]time.Time
	lastErr error
	last    *httptest.ResponseRecorder
}

func newRememberBrowser(t *testing.T, scheme *SessionScheme) *rememberBrowser {
	t.Helper()
	b := &rememberBrowser{t: t, cookies: map[string]*http.Cookie{}, expires: map[string]time.Time{}}
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Post("/login", func(c *router.Context) error {
		if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}, true); err != nil {
			return err
		}
		return c.String(http.StatusOK, "in")
	})
	r.Get("/check", func(c *router.Context) error {
		ok, err := scheme.CheckWithError(c.Request)
		b.lastErr = err
		if !ok {
			return c.String(http.StatusUnauthorized, "out")
		}
		return c.String(http.StatusOK, "in")
	})
	b.handler = r
	return b
}

func (b *rememberBrowser) do(method, path string) *httptest.ResponseRecorder {
	b.t.Helper()
	now := sessionclock.Now()
	req := httptest.NewRequest(method, path, nil)
	for name, c := range b.cookies {
		if exp, ok := b.expires[name]; ok && !now.Before(exp) {
			delete(b.cookies, name)
			delete(b.expires, name)
			continue
		}
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		switch {
		case c.MaxAge < 0:
			delete(b.cookies, c.Name)
			delete(b.expires, c.Name)
		case c.MaxAge > 0:
			b.cookies[c.Name] = c
			b.expires[c.Name] = now.Add(time.Duration(c.MaxAge) * time.Second)
		default:
			b.cookies[c.Name] = c
			delete(b.expires, c.Name)
		}
	}
	b.last = w
	return w
}

func (b *rememberBrowser) signedIn() bool {
	b.t.Helper()
	return b.do(http.MethodGet, "/check").Code == http.StatusOK
}

// responseCookie returns the named cookie the last response set, or nil.
func (b *rememberBrowser) responseCookie(name string) *http.Cookie {
	for _, c := range b.last.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func userStoreOf(t *testing.T, scheme *SessionScheme) *revokeTestStore {
	t.Helper()
	us, ok := scheme.loadUserStore().(*revokeTestStore)
	if !ok {
		t.Fatalf("user store is %T, want *revokeTestStore", scheme.loadUserStore())
	}
	return us
}

// onlyRecord returns the one live server record u1 holds other than
// the record with id except.
func onlyRecord(t *testing.T, mem *session.MemoryStore, except string) *auth.StoredSession {
	t.Helper()
	list, err := mem.ListForUser(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ListForUser: %v", err)
	}
	var ids []string
	for _, m := range list {
		if m.ID != except {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) != 1 {
		t.Fatalf("u1 has %d live server records besides %q, want 1", len(ids), except)
	}
	rec, err := mem.Get(context.Background(), ids[0])
	if err != nil {
		t.Fatalf("Get(%q): %v", ids[0], err)
	}
	return rec
}

// Ticking remember-me gives the remember cookie the remember lifetime,
// not the session's idle lifetime: with the starter's 120-minute idle
// timeout the remember cookie still lasts 30 days.
func TestRememberLifetime_CookieOutlivesIdleLifetime(t *testing.T) {
	installLifetimeClock(t)
	scheme, _ := newLifetimeScheme(t, 120, 0, true)
	b := newRememberBrowser(t, scheme)
	b.do(http.MethodPost, "/login")

	sess, rem := b.responseCookie(sessionCookieName), b.responseCookie(rememberCookieName)
	if sess == nil || rem == nil {
		t.Fatalf("login set session=%v remember=%v", sess, rem)
	}
	if sess.MaxAge != 7200 {
		t.Fatalf("session cookie MaxAge = %d, want 7200 (idle lifetime)", sess.MaxAge)
	}
	if rem.MaxAge != defaultRememberSeconds {
		t.Fatalf("remember cookie MaxAge = %d, want %d (remember lifetime, not the idle lifetime)", rem.MaxAge, defaultRememberSeconds)
	}
}

// A remembered user whose session ended under the lifetime policy is
// signed back in by the remember cookie on a rotated session id with a
// fresh server record, whether the session ended on the cookie (the
// browser dropped it after the idle lifetime) or on the server record
// (the store reports it expired while the cookie is still live).
func TestRememberLifetime_EndedSessionRevivedByRememberCookie(t *testing.T) {
	tests := []struct {
		name      string
		withStore bool
		// end ends the session and returns how far the clock moved.
		end func(t *testing.T, clock *lifetimeClock, mem *session.MemoryStore)
	}{
		{
			name:      "session cookie idled out, server store",
			withStore: true,
			end: func(_ *testing.T, clock *lifetimeClock, _ *session.MemoryStore) {
				clock.advance(121 * time.Minute)
			},
		},
		{
			name: "session cookie idled out, cookie only",
			end: func(_ *testing.T, clock *lifetimeClock, _ *session.MemoryStore) {
				clock.advance(121 * time.Minute)
			},
		},
		{
			name:      "server record expired, session cookie live",
			withStore: true,
			end: func(t *testing.T, clock *lifetimeClock, mem *session.MemoryStore) {
				clock.advance(30 * time.Minute)
				rec := *onlyRecord(t, mem, "")
				rec.ExpiresAt = clock.Now().Add(-time.Second)
				if err := mem.Put(context.Background(), &rec); err != nil {
					t.Fatalf("Put: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeScheme(t, 120, 0, tt.withStore)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			var oldID string
			if mem != nil {
				oldID = onlyRecord(t, mem, "").ID
			}
			oldSession := b.cookies[sessionCookieName].Value
			oldRemember := b.cookies[rememberCookieName].Value

			tt.end(t, clock, mem)

			if !b.signedIn() {
				t.Fatalf("remembered user not signed back in after the session ended (err=%v)", b.lastErr)
			}
			sess := b.responseCookie(sessionCookieName)
			if sess == nil || sess.Value == oldSession {
				t.Fatal("revival did not issue a new session cookie")
			}
			rem := b.responseCookie(rememberCookieName)
			if rem == nil || rem.Value == oldRemember || rem.MaxAge != defaultRememberSeconds {
				t.Fatalf("revival did not rotate the remember cookie with the remember lifetime: %+v", rem)
			}
			if mem != nil {
				rec := onlyRecord(t, mem, oldID)
				if !rec.CreatedAt.Equal(clock.Now()) {
					t.Fatalf("new record CreatedAt = %v, want the revival time %v", rec.CreatedAt, clock.Now())
				}
			}
			// The revived session is an ordinary signed-in session.
			clock.advance(5 * time.Minute)
			if !b.signedIn() {
				t.Fatalf("revived session not signed in on the next request (err=%v)", b.lastErr)
			}
		})
	}
}

// Revocation stays authoritative over remember-me: after
// RevokeAllSessions neither the same cookies nor the remember cookie alone
// sign the user back in.
func TestRememberLifetime_RevokeAllSessionsIsNeverRevived(t *testing.T) {
	for _, later := range []time.Duration{0, 121 * time.Minute} {
		t.Run("after "+later.String(), func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeScheme(t, 120, 0, true)
			mgr := auth.NewManager()
			mgr.SetServerSessionStore(mem)
			mgr.RegisterScheme("web", scheme)
			b := newRememberBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			stolenRemember := *b.cookies[rememberCookieName]

			if err := mgr.RevokeAllSessions(context.Background(), "u1"); err != nil {
				t.Fatalf("RevokeAllSessions: %v", err)
			}
			clock.advance(later)
			if b.signedIn() {
				t.Fatal("same cookies signed in after RevokeAllSessions")
			}
			// The session cookie is still live only on the immediate request.
			if later == 0 && !errors.Is(b.lastErr, auth.ErrSessionRevoked) {
				t.Fatalf("revoked session reported as %v, want auth.ErrSessionRevoked", b.lastErr)
			}
			b.cookies = map[string]*http.Cookie{rememberCookieName: &stolenRemember}
			b.expires = map[string]time.Time{}
			if b.signedIn() {
				t.Fatal("remember cookie alone signed in after RevokeAllSessions")
			}
		})
	}
}

// A single revoked session is not revived by its remember cookie: the
// request is refused as revoked, and the remember credential it presented
// is burned and deleted, so replaying it once the session cookie has gone
// signs nobody in.
func TestRememberLifetime_RevokedSessionBurnsItsRememberCredential(t *testing.T) {
	clock := installLifetimeClock(t)
	scheme, mem := newLifetimeScheme(t, 120, 0, true)
	users := userStoreOf(t, scheme)
	mgr := auth.NewManager()
	mgr.SetServerSessionStore(mem)
	mgr.RegisterScheme("web", scheme)
	b := newRememberBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	stolenRemember := *b.cookies[rememberCookieName]

	if err := mgr.RevokeSession(context.Background(), onlyRecord(t, mem, "").ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	clock.advance(time.Minute)
	if b.signedIn() {
		t.Fatal("revoked session signed in")
	}
	if !errors.Is(b.lastErr, auth.ErrSessionRevoked) {
		t.Fatalf("revoked session reported as %v, want auth.ErrSessionRevoked", b.lastErr)
	}
	if got := users.users["u1"].rememberToken; got != "" {
		t.Fatalf("remember token still stored after the revoked session presented it: %q", got)
	}
	if rem := b.responseCookie(rememberCookieName); rem == nil || rem.MaxAge >= 0 {
		t.Fatalf("revoked request did not delete the remember cookie: %+v", rem)
	}

	clock.advance(121 * time.Minute)
	b.cookies = map[string]*http.Cookie{rememberCookieName: &stolenRemember}
	b.expires = map[string]time.Time{}
	if b.signedIn() {
		t.Fatal("remember cookie of a revoked session signed in once the session cookie was gone")
	}
}
