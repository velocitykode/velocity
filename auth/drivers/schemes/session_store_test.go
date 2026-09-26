package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// warnLog records the scheme's warnings.
type warnLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *warnLog) Info(string, ...any)  {}
func (l *warnLog) Error(string, ...any) {}
func (l *warnLog) Warn(msg string, _ ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, msg)
}

func (l *warnLog) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, s) {
			return true
		}
	}
	return false
}

// storeScheme builds a session scheme through NewSessionScheme. With
// serverSide it keeps sessions in a session.ServerStore over a cache-backed
// record store, installed the way velocity.New does for
// SESSION_STORE=server; otherwise it keeps the default cookie store.
func storeScheme(t *testing.T, serverSide bool) (*SessionScheme, *session.CacheStore) {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	cfg := auth.SessionConfig{
		Name:         "vel_session",
		IdleLifetime: 60,
		Path:         "/",
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
	if !serverSide {
		scheme, err := NewSessionScheme(users, cfg, enc)
		if err != nil {
			t.Fatalf("NewSessionScheme: %v", err)
		}
		return scheme, nil
	}
	backend := drivers.NewMemoryStore("sessions")
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })
	records, err := session.NewCacheStore(backend)
	if err != nil {
		t.Fatalf("NewCacheStore: %v", err)
	}
	store, err := session.NewServerStore(cfg, records)
	if err != nil {
		t.Fatalf("NewServerStore: %v", err)
	}
	scheme, err := NewSessionScheme(users, cfg, enc, WithSessionStore(store))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetServerSessionStore(records)
	return scheme, records
}

// storeBrowser drives a scheme through the session save seam with
// handlers that use the session API only, keeping cookies across requests.
type storeBrowser struct {
	t       *testing.T
	handler http.Handler
	cookies map[string]*http.Cookie
	lastErr error
	// sessionCookies records every session Set-Cookie line sent.
	sessionCookies []string
}

func newStoreBrowser(t *testing.T, scheme *SessionScheme) *storeBrowser {
	t.Helper()
	b := &storeBrowser{t: t, cookies: map[string]*http.Cookie{}}
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Get("/public", func(c *router.Context) error { return c.String(http.StatusOK, "public") })
	r.Post("/login", func(c *router.Context) error {
		if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}); err != nil {
			return err
		}
		return c.String(http.StatusOK, "in")
	})
	r.Post("/logout", func(c *router.Context) error {
		if err := scheme.Logout(c.Response, c.Request); err != nil {
			return err
		}
		return c.String(http.StatusOK, "out")
	})
	r.Post("/draft", func(c *router.Context) error {
		scheme.Session(c.Request).Put("draft", c.Request.URL.Query().Get("v"))
		scheme.Session(c.Request).Flash("status", "saved")
		return c.String(http.StatusOK, "ok")
	})
	r.Get("/draft", func(c *router.Context) error {
		s := scheme.Session(c.Request)
		draft, _ := s.Get("draft").(string)
		status, _ := s.GetFlash("status").(string)
		return c.String(http.StatusOK, status+":"+draft)
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

func (b *storeBrowser) do(method, path string) *httptest.ResponseRecorder {
	b.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, req)
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, "vel_session=") {
			b.sessionCookies = append(b.sessionCookies, line)
		}
	}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c
	}
	return w
}

// Handlers read and write the session the same way whichever store holds
// it; with the server store a 10 KB bag round-trips behind a short cookie,
// and the session keeps one server record, the one revocation reads.
func TestSessionScheme_WithSessionStore_HandlersUseTheSessionUnchanged(t *testing.T) {
	tests := []struct {
		name       string
		serverSide bool
		draftBytes int
	}{
		{"cookie store", false, 500},
		{"server store", true, 10 * 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, records := storeScheme(t, tt.serverSide)
			b := newStoreBrowser(t, scheme)
			draft := strings.Repeat("d", tt.draftBytes)

			b.do(http.MethodGet, "/public")
			if code := b.do(http.MethodPost, "/login").Code; code != http.StatusOK {
				t.Fatalf("login: status %d", code)
			}
			if code := b.do(http.MethodPost, "/draft?v="+draft).Code; code != http.StatusOK {
				t.Fatalf("draft: status %d", code)
			}
			if body := b.do(http.MethodGet, "/draft").Body.String(); body != "saved:"+draft {
				t.Fatalf("GET /draft = %d bytes %.40q..., want the flash and the %d-byte draft", len(body), body, tt.draftBytes)
			}
			if body := b.do(http.MethodGet, "/draft").Body.String(); body != ":"+draft {
				t.Fatalf("flash not consumed once: %.40q", body)
			}
			if !strings.HasPrefix(b.do(http.MethodGet, "/check").Body.String(), "in") {
				t.Fatalf("not signed in after the round trips: %v", b.lastErr)
			}
			if !tt.serverSide {
				return
			}

			for _, line := range b.sessionCookies {
				if len(line) >= 200 {
					t.Fatalf("server store sent a %d-byte session cookie, want under 200: %q", len(line), line)
				}
			}
			// One record per session: the visit's signed-out record was
			// removed at sign-in, and the user's one record holds the data.
			list, err := records.ListForUser(context.Background(), "u1")
			if err != nil || len(list) != 1 {
				t.Fatalf("ListForUser = %v, %v; want the one signed-in record", list, err)
			}
			rec, err := records.Get(context.Background(), list[0].ID)
			if err != nil {
				t.Fatalf("record: %v", err)
			}
			data, _ := rec.Data["data"].(map[string]any)
			if data["draft"] != draft || data[auth.UserIDSessionKey] != "u1" {
				t.Fatalf("the user's record does not hold the session data: %v", rec.Data)
			}
			if b.cookies["vel_session"].Value != rec.ID {
				t.Fatal("the cookie does not carry the record's session id")
			}

			// Revoking the record ends the session and is reported as a
			// revocation, though the data went with the record.
			if err := records.Delete(context.Background(), rec.ID); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if code := b.do(http.MethodGet, "/check").Code; code != http.StatusUnauthorized || !errors.Is(b.lastErr, auth.ErrSessionRevoked) {
				t.Fatalf("after revocation: status %d err %v, want 401 and ErrSessionRevoked", code, b.lastErr)
			}
		})
	}
}

// Logout with the server store removes the session's record and deletes
// the cookie.
func TestSessionScheme_ServerStoreLogoutRemovesTheRecord(t *testing.T) {
	scheme, records := storeScheme(t, true)
	b := newStoreBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	id := b.cookies["vel_session"].Value
	b.do(http.MethodPost, "/logout")
	if _, ok := b.cookies["vel_session"]; ok {
		t.Fatal("session cookie survived logout")
	}
	if _, err := records.Get(context.Background(), id); !errors.Is(err, auth.ErrSessionNotFound) {
		t.Fatalf("record survived logout: %v", err)
	}
}

// A session that would not fit the 4096-byte cookie is not sent: the
// response carries no session cookie, the failure is logged, and the
// browser keeps the session it had.
func TestSessionMiddleware_OversizeCookieIsNotSent(t *testing.T) {
	scheme, _ := storeScheme(t, false)
	logs := &warnLog{}
	scheme.SetLogger(logs)
	b := newStoreBrowser(t, scheme)
	b.do(http.MethodPost, "/login")
	before := b.cookies["vel_session"].Value

	sent := len(b.sessionCookies)
	b.do(http.MethodPost, "/draft?v="+strings.Repeat("x", 4000))
	if len(b.sessionCookies) != sent {
		t.Fatalf("an oversize session cookie was sent (%d bytes)", len(b.sessionCookies[len(b.sessionCookies)-1]))
	}
	if !logs.contains("4096 bytes") {
		t.Fatalf("oversize save not logged; warnings: %v", logs.lines)
	}
	if b.cookies["vel_session"].Value != before {
		t.Fatal("the session cookie changed")
	}
	if !strings.HasPrefix(b.do(http.MethodGet, "/check").Body.String(), "in") {
		t.Fatalf("the previous session was lost: %v", b.lastErr)
	}
}

// The server record's grace covers the activity debounce, so the record
// always outlives the cookie the seam last wrote.
func TestSessionRecordGrace_CoversTheActivityDebounce(t *testing.T) {
	cfg := auth.SessionConfig{IdleLifetime: 120}
	now := time.Now()
	if grace := cfg.RecordExpiresAt(now, now).Sub(cfg.ExpiresAt(now, now)); grace < lastSeenDebounce {
		t.Fatalf("record grace %v is shorter than the debounce %v", grace, lastSeenDebounce)
	}
}
