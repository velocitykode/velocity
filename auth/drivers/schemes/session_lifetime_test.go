package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/internal/sessionclock"
	"github.com/velocitykode/velocity/router"
)

// lifetimeClock is a movable session clock (sessionclock.Set) shared by
// the cookie store, the scheme and the memory server store, so a test can
// walk a session through hours of activity and idleness without sleeping.
type lifetimeClock struct {
	mu  sync.Mutex
	now time.Time
}

func installLifetimeClock(t *testing.T) *lifetimeClock {
	t.Helper()
	c := &lifetimeClock{now: time.Now()}
	t.Cleanup(sessionclock.Set(c.Now))
	return c
}

func (c *lifetimeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lifetimeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// lifetimeBrowser drives a scheme through the session save seam the way a
// browser does: it keeps the cookies each response sets and sends them on
// the next request.
type lifetimeBrowser struct {
	t       *testing.T
	scheme  *SessionScheme
	handler http.Handler
	cookies map[string]*http.Cookie
	// lastErr is the CheckWithError error the last /check request saw.
	lastErr error
}

func newLifetimeBrowser(t *testing.T, scheme *SessionScheme) *lifetimeBrowser {
	t.Helper()
	b := &lifetimeBrowser{t: t, scheme: scheme, cookies: map[string]*http.Cookie{}}
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Post("/login", func(c *router.Context) error {
		if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}); err != nil {
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
	r.Get("/public", func(c *router.Context) error {
		return c.String(http.StatusOK, "public")
	})
	b.handler = r
	return b
}

// do sends one request and returns the response recorder.
func (b *lifetimeBrowser) do(method, path string) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c
	}
	return w
}

// signedIn reports whether a /check request is authenticated.
func (b *lifetimeBrowser) signedIn() bool {
	b.t.Helper()
	return b.do(http.MethodGet, "/check").Code == http.StatusOK
}

// sessionCookie returns the session cookie the last response set, or nil.
func sessionCookieOf(w *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range w.Result().Cookies() {
		if c.Name == "vel_session" {
			return c
		}
	}
	return nil
}

// newLifetimeScheme builds a session scheme with the given idle and
// absolute lifetimes (minutes) and, when withStore, a memory server store.
func newLifetimeScheme(t *testing.T, idle, absolute int, withStore bool) (*SessionScheme, *session.MemoryStore) {
	t.Helper()
	scheme, _ := newRevokeScheme(t, nil)
	scheme.config.IdleLifetime = idle
	scheme.config.AbsoluteLifetime = absolute
	st, err := session.NewCookieStore(scheme.config, scheme.encryptor)
	if err != nil {
		t.Fatal(err)
	}
	scheme.store = st
	if !withStore {
		return scheme, nil
	}
	mem := session.NewMemoryStore()
	t.Cleanup(func() { _ = mem.Close(context.Background()) })
	scheme.SetServerSessionStore(mem)
	return scheme, mem
}

var lifetimeModes = []struct {
	name      string
	withStore bool
}{
	{"server store", true},
	{"cookie only", false},
}

// An active session (a request every 5 minutes) outlives the idle
// lifetime, on the cookie and on the server record, and ends at the
// absolute cap, reported as expiry.
func TestSessionLifetime_ActiveSessionSlidesUntilAbsoluteCap(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, _ := newLifetimeScheme(t, 120, 480, mode.withStore)
			b := newLifetimeBrowser(t, scheme)
			if code := b.do(http.MethodPost, "/login").Code; code != http.StatusOK {
				t.Fatalf("login: status %d", code)
			}

			var endedAt time.Duration
			for elapsed := 5 * time.Minute; elapsed <= 10*time.Hour; elapsed += 5 * time.Minute {
				clock.advance(5 * time.Minute)
				if !b.signedIn() {
					endedAt = elapsed
					break
				}
			}
			if endedAt == 0 {
				t.Fatal("active session never ended; the absolute cap (480m) did not apply")
			}
			if endedAt <= 120*time.Minute {
				t.Fatalf("active session ended at +%v, within the idle lifetime from sign-in (120m): activity did not slide it", endedAt)
			}
			// The first request past the cap (+480m) is refused.
			if endedAt <= 480*time.Minute || endedAt > 485*time.Minute {
				t.Fatalf("active session ended at +%v, want the first request past the absolute cap (+8h0m0s)", endedAt)
			}
			if !errors.Is(b.lastErr, auth.ErrSessionExpired) {
				t.Fatalf("absolute cap reported as %v, want auth.ErrSessionExpired", b.lastErr)
			}
		})
	}
}

// A session idle for longer than the idle lifetime ends on its next
// request, on the cookie and on the server record alike, reported as
// expiry rather than revocation.
func TestSessionLifetime_IdleSessionEndsOnNextRequest(t *testing.T) {
	for _, mode := range lifetimeModes {
		t.Run(mode.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeScheme(t, 120, 480, mode.withStore)
			b := newLifetimeBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			clock.advance(90 * time.Minute)
			if !b.signedIn() {
				t.Fatal("session ended inside the idle window")
			}
			var id string
			if mem != nil {
				list, err := mem.ListForUser(context.Background(), "u1")
				if err != nil || len(list) != 1 {
					t.Fatalf("server records: %v, %v", list, err)
				}
				id = list[0].ID
			}

			// Idle for longer than the idle lifetime (and the server
			// record's one-minute grace).
			clock.advance(125 * time.Minute)
			if b.signedIn() {
				t.Fatal("session idle for 125m still signed in with a 120m idle lifetime")
			}
			if !errors.Is(b.lastErr, auth.ErrSessionExpired) {
				t.Fatalf("idle expiry reported as %v, want auth.ErrSessionExpired", b.lastErr)
			}
			if mem != nil {
				if _, err := mem.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionExpired) && !errors.Is(err, auth.ErrSessionNotFound) {
					t.Fatalf("server record of the idle session still live: %v", err)
				}
			}
		})
	}
}

// The server record enforces the lifetime policy on its own: a record past
// its idle window or its absolute cap (CreatedAt) ends the session even
// when the cookie presented with it is still inside its own windows.
func TestSessionLifetime_ServerRecordEnforcesPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(rec *auth.StoredSession, now time.Time)
	}{
		{"idle window passed", func(rec *auth.StoredSession, now time.Time) {
			rec.ExpiresAt = now.Add(-time.Second)
		}},
		{"absolute cap passed on CreatedAt", func(rec *auth.StoredSession, now time.Time) {
			rec.CreatedAt = now.Add(-481 * time.Minute)
			rec.ExpiresAt = now.Add(time.Hour)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, mem := newLifetimeScheme(t, 120, 480, true)
			b := newLifetimeBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			list, _ := mem.ListForUser(context.Background(), "u1")
			if len(list) != 1 {
				t.Fatalf("server records: %v", list)
			}
			rec, err := mem.Get(context.Background(), list[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(rec, clock.Now())
			rec.LastSeenAt = clock.Now()
			if err := mem.Put(context.Background(), rec); err != nil {
				t.Fatal(err)
			}
			if b.signedIn() {
				t.Fatal("server record past the lifetime policy still authenticates")
			}
			if !errors.Is(b.lastErr, auth.ErrSessionExpired) {
				t.Fatalf("server-side expiry reported as %v, want auth.ErrSessionExpired", b.lastErr)
			}
		})
	}
}

// Administrative revocation stays distinct from expiry.
func TestSessionLifetime_RevocationIsNotExpiry(t *testing.T) {
	installLifetimeClock(t)
	scheme, mem := newLifetimeScheme(t, 120, 480, true)
	b := newLifetimeBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	if err := mem.DeleteAllForUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if b.signedIn() {
		t.Fatal("revoked session still signed in")
	}
	if !errors.Is(b.lastErr, auth.ErrSessionRevoked) || errors.Is(b.lastErr, auth.ErrSessionExpired) {
		t.Fatalf("revocation reported as %v, want auth.ErrSessionRevoked only", b.lastErr)
	}
}

// Activity re-issues the session cookie through the seam, at most once per
// debounce interval, so its Max-Age and IssuedAt slide with the server
// record; a request inside the interval writes no cookie.
func TestSessionLifetime_ActivityReissuesCookieDebounced(t *testing.T) {
	for _, path := range []string{"/check", "/public"} {
		for _, mode := range lifetimeModes {
			t.Run(path+"/"+mode.name, func(t *testing.T) {
				clock := installLifetimeClock(t)
				scheme, mem := newLifetimeScheme(t, 120, 480, mode.withStore)
				b := newLifetimeBrowser(t, scheme)
				b.do(http.MethodPost, "/login")

				clock.advance(30 * time.Second)
				if c := sessionCookieOf(b.do(http.MethodGet, path)); c != nil {
					t.Fatalf("session cookie re-issued inside the debounce interval: %v", c)
				}

				clock.advance(31 * time.Second)
				c := sessionCookieOf(b.do(http.MethodGet, path))
				if c == nil {
					t.Fatal("session cookie not re-issued after the debounce interval")
				}
				if c.MaxAge != 120*60 {
					t.Fatalf("re-issued cookie MaxAge = %d, want %d", c.MaxAge, 120*60)
				}
				if mem != nil {
					list, _ := mem.ListForUser(context.Background(), "u1")
					if len(list) != 1 {
						t.Fatalf("server records: %v", list)
					}
					if !list[0].LastSeenAt.Equal(clock.Now()) {
						t.Fatalf("server record not touched with the cookie re-issue: LastSeenAt %v, now %v", list[0].LastSeenAt, clock.Now())
					}
					if list[0].ExpiresAt.Before(clock.Now().Add(120 * time.Minute)) {
						t.Fatalf("server record ExpiresAt %v earlier than the re-issued cookie's end %v", list[0].ExpiresAt, clock.Now().Add(120*time.Minute))
					}
				}
			})
		}
	}
}

// Near the absolute cap the re-issued cookie's Max-Age is the time left,
// not a full idle window, so the browser drops it when the server would
// reject it.
func TestSessionLifetime_CookieMaxAgeStopsAtAbsoluteCap(t *testing.T) {
	clock := installLifetimeClock(t)
	scheme, _ := newLifetimeScheme(t, 120, 480, false)
	b := newLifetimeBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	for elapsed := time.Duration(0); elapsed < 450*time.Minute; elapsed += 30 * time.Minute {
		clock.advance(30 * time.Minute)
		b.do(http.MethodGet, "/public")
	}
	clock.advance(10 * time.Minute) // +460m: 20 minutes left of the cap
	c := sessionCookieOf(b.do(http.MethodGet, "/public"))
	if c == nil {
		t.Fatal("no cookie re-issued")
	}
	if c.MaxAge != 20*60 {
		t.Fatalf("MaxAge = %d near the absolute cap, want %d (the time left)", c.MaxAge, 20*60)
	}
}
