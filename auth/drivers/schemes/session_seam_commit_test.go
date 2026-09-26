package schemes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/cache/drivers"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
)

// sessionLines returns the vel_session Set-Cookie lines the client
// received (the recorder's committed header snapshot).
func sessionLines(w *httptest.ResponseRecorder) []string {
	var out []string
	for _, line := range w.Result().Header.Values("Set-Cookie") {
		if strings.HasPrefix(line, "vel_session=") {
			out = append(out, line)
		}
	}
	return out
}

// runOnPlainWriter drives chain over a writer without the router's
// pre-commit hook, the way a custom stack or a recorder-backed context
// does, carrying the browser's cookies.
func runOnPlainWriter(t *testing.T, chain router.HandlerFunc, method, path string, cookies map[string]*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for _, c := range cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	w := httptest.NewRecorder()
	if err := chain(router.NewContext(w, req)); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return w
}

// A handler that signs in and writes its response on a writer without the
// pre-commit hook still delivers the session cookie: the seam saves before
// the first byte, not after the handler, when the headers are gone.
func TestSessionMiddleware_PlainWriterDeliversTheSessionCookie(t *testing.T) {
	writes := []struct {
		name  string
		write func(c *router.Context) error
	}{
		{"json", func(c *router.Context) error { return c.JSON(http.StatusOK, map[string]string{"ok": "in"}) }},
		{"redirect", func(c *router.Context) error { return c.Redirect(http.StatusSeeOther, "/home") }},
		{"no body", func(c *router.Context) error { return nil }},
	}
	for _, serverSide := range []bool{false, true} {
		for _, tt := range writes {
			name := tt.name
			if serverSide {
				name += "/server store"
			}
			t.Run(name, func(t *testing.T) {
				scheme, _ := storeScheme(t, serverSide)
				chain := scheme.SessionMiddleware()(func(c *router.Context) error {
					if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}); err != nil {
						return err
					}
					return tt.write(c)
				})
				w := runOnPlainWriter(t, chain, http.MethodPost, "/login", nil)
				lines := sessionLines(w)
				if len(lines) != 1 {
					t.Fatalf("login on a plain writer sent %d session cookies with the response, want 1: %v", len(lines), lines)
				}
				// The cookie signs the visitor in.
				check := scheme.SessionMiddleware()(func(c *router.Context) error {
					if !scheme.Check(c.Request) {
						return c.String(http.StatusUnauthorized, "out")
					}
					return c.String(http.StatusOK, "in")
				})
				jar := map[string]*http.Cookie{}
				for _, c := range w.Result().Cookies() {
					jar[c.Name] = c
				}
				if code := runOnPlainWriter(t, check, http.MethodGet, "/check", jar).Code; code != http.StatusOK {
					t.Fatalf("GET /check with the delivered cookie = %d, want 200", code)
				}
			})
		}
	}
}

// The session middleware mounted twice around one handler saves the
// session once, on both writer kinds, including a destroyed session: the
// seam's commit belongs to the request, not to each middleware.
func TestSessionMiddleware_NestedMiddlewareSavesOnce(t *testing.T) {
	for _, hooked := range []bool{true, false} {
		for _, op := range []string{"logout", "put"} {
			for _, body := range []bool{true, false} {
				name := op + "/plain writer"
				if hooked {
					name = op + "/router writer"
				}
				if !body {
					name += "/no body"
				}
				t.Run(name, func(t *testing.T) {
					scheme, _ := storeScheme(t, false)
					handler := func(c *router.Context) error {
						if op == "logout" {
							if err := scheme.Logout(c.Response, c.Request); err != nil {
								return err
							}
						} else {
							scheme.Session(c.Request).Put("k", "v")
						}
						if !body {
							return nil
						}
						return c.String(http.StatusOK, "done")
					}
					login := func(c *router.Context) error {
						if err := scheme.Login(c.Response, c.Request, &revokeTestUser{id: "u1"}); err != nil {
							return err
						}
						if !body {
							return nil
						}
						return c.String(http.StatusOK, "in")
					}

					var w *httptest.ResponseRecorder
					if hooked {
						r := router.New()
						r.Use(scheme.SessionMiddleware())
						r.Use(scheme.SessionMiddleware())
						r.Post("/login", login)
						r.Post("/op", handler)
						lw := httptest.NewRecorder()
						r.ServeHTTP(lw, httptest.NewRequest(http.MethodPost, "/login", nil))
						req := httptest.NewRequest(http.MethodPost, "/op", nil)
						for _, c := range lw.Result().Cookies() {
							req.AddCookie(c)
						}
						w = httptest.NewRecorder()
						r.ServeHTTP(w, req)
					} else {
						mw := scheme.SessionMiddleware()
						lw := runOnPlainWriter(t, mw(mw(login)), http.MethodPost, "/login", nil)
						jar := map[string]*http.Cookie{}
						for _, c := range lw.Result().Cookies() {
							jar[c.Name] = c
						}
						w = runOnPlainWriter(t, mw(mw(handler)), http.MethodPost, "/op", jar)
					}
					if lines := sessionLines(w); len(lines) != 1 {
						t.Fatalf("%s through two session middlewares sent %d session cookies, want 1: %v", op, len(lines), lines)
					}
				})
			}
		}
	}
}

// Deleting the session cookie from a handler ends the session: activity
// renewal past the debounce cannot issue it again in the same response,
// and a server record goes with it.
func TestSessionMiddleware_DeletedSessionCookieIsNotRenewed(t *testing.T) {
	for _, serverSide := range []bool{false, true} {
		name := "cookie store"
		if serverSide {
			name = "server store"
		}
		t.Run(name, func(t *testing.T) {
			clock := installLifetimeClock(t)
			scheme, records := storeScheme(t, serverSide)
			b := newStoreBrowser(t, scheme)
			b.do(http.MethodPost, "/login")
			id := b.cookies["vel_session"].Value

			r := router.New()
			r.Use(scheme.SessionMiddleware())
			r.Get("/forget", func(c *router.Context) error {
				c.DeleteCookie("vel_session")
				return c.String(http.StatusOK, "forgotten")
			})
			b.handler = r

			clock.advance(lastSeenDebounce + time.Second)
			w := b.do(http.MethodGet, "/forget")
			lines := sessionLines(w)
			if len(lines) == 0 {
				t.Fatal("no session cookie line: the deletion was lost")
			}
			last, err := http.ParseSetCookie(lines[len(lines)-1])
			if err != nil {
				t.Fatalf("parse %q: %v", lines[len(lines)-1], err)
			}
			if last.MaxAge >= 0 {
				t.Fatalf("the response re-issued the session cookie after the handler deleted it: %v", lines)
			}
			if _, ok := b.cookies["vel_session"]; ok {
				t.Fatal("the browser still holds the session cookie")
			}
			if serverSide {
				if _, err := records.Get(context.Background(), id); err == nil {
					t.Fatal("the deleted session's server record is still live")
				}
			}
		})
	}
}

// A session scheme built with WithSessionStore(NewServerStore(cfg,
// records)) and nothing else signs in: the scheme takes the record store
// the server store keeps sessions in as its own.
func TestWithSessionStore_ServerStoreAloneSignsIn(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	cfg := auth.SessionConfig{Name: "vel_session", IdleLifetime: 60, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
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
	users := &revokeTestStore{users: map[string]*revokeTestUser{"u1": {id: "u1"}}}
	scheme, err := NewSessionScheme(users, cfg, enc, WithSessionStore(store))
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	b := newStoreBrowser(t, scheme)
	b.do(http.MethodGet, "/public")
	if code := b.do(http.MethodPost, "/login").Code; code != http.StatusOK {
		t.Fatalf("POST /login = %d, want 200", code)
	}
	if !strings.HasPrefix(b.do(http.MethodGet, "/check").Body.String(), "in") {
		t.Fatalf("not signed in with the server store passed alone: %v", b.lastErr)
	}
	list, err := records.ListForUser(context.Background(), "u1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListForUser = %v, %v; want the sign-in record", list, err)
	}
}
