package velocity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/router"
)

// csrfBagInstance boots one app instance the way a starter does
// (ConfigFromEnv, AUTH_SCHEME=web, CSRF on the router) for the given
// SESSION_STORE, without Bootstrap. records, when non-nil, stands in for
// the shared cache (redis) every instance of a deploy talks to: it
// replaces the per-process memory cache records so a second instance sees
// the first one's sessions.
func csrfBagInstance(t *testing.T, sessionStore string, records auth.ServerSessionStore, singleUse bool) *App {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":               "development", // "testing" bypasses CSRF validation
		"APP_KEY":               strings.Repeat("k", 32),
		"AUTH_SCHEME":           "web",
		"LOG_DRIVER":            "null",
		"CACHE_DRIVER":          "memory",
		"QUEUE_DRIVER":          "memory",
		"MAIL_DRIVER":           "log",
		"SESSION_SECURE":        "false",
		"SESSION_IDLE_LIFETIME": "120",
		"SESSION_STORE":         sessionStore,
	} {
		t.Setenv(k, v)
	}
	cfg := ConfigFromEnv()
	cfg.CSRF.SingleUse = singleUse
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	if records != nil {
		a.Auth.(*auth.Manager).SetServerSessionStore(records)
	}
	a.Router.Use(a.Services.CSRF.(*csrf.CSRF).RouterMiddleware())
	h := func(c *router.Context) error { return c.String(http.StatusOK, "ok") }
	a.Router.Get("/form", h)
	a.Router.Post("/form", h)
	a.Router.Get("/token", func(c *router.Context) error {
		tok, err := csrf.TokenForRequest(c.Request)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, tok)
	})
	a.Router.Post("/login", func(c *router.Context) error {
		if err := auth.FromServices(a.Services).Login(c.Response, c.Request, &saveSeamUser{id: 9}); err != nil {
			return err
		}
		return c.String(http.StatusOK, "in")
	})
	a.Router.Post("/logout", func(c *router.Context) error {
		if err := auth.FromServices(a.Services).Logout(c.Response, c.Request); err != nil {
			return err
		}
		return c.String(http.StatusOK, "out")
	})
	return a
}

// csrfBagJar is the browser side of the flow: the cookies it holds and the
// XSRF-TOKEN value it echoes.
type csrfBagJar map[string]string

func (j csrfBagJar) send(t *testing.T, h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	for name, value := range j {
		r.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	if token != "" {
		r.Header.Set("X-CSRF-Token", token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
			continue
		}
		j[c.Name] = c.Value
	}
	return w
}

// xsrf returns the token the browser would echo: the XSRF-TOKEN cookie,
// URL-decoded.
func (j csrfBagJar) xsrf(t *testing.T) string {
	t.Helper()
	raw, ok := j["XSRF-TOKEN"]
	if !ok {
		t.Fatal("no XSRF-TOKEN cookie held")
	}
	tok, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestCSRFToken_ValidatesOnAnotherInstance pins that the CSRF token lives
// where the session lives: a token issued by one app instance validates on
// a second instance built from the same env and APP_KEY (a deploy restart,
// or another replica without sticky routing), with the cookie session store
// and with the server session store over a shared record store.
func TestCSRFToken_ValidatesOnAnotherInstance(t *testing.T) {
	for _, tt := range []struct {
		name  string
		store string
	}{
		{"cookie session store", "cookie"},
		{"server session store", "server"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var records auth.ServerSessionStore
			if tt.store == "server" {
				records = session.NewMemoryStore()
			}
			before := csrfBagInstance(t, tt.store, records, false)
			jar := csrfBagJar{}
			if w := jar.send(t, before.Router, http.MethodGet, "/form", ""); w.Code != http.StatusOK {
				t.Fatalf("GET /form = %d", w.Code)
			}
			issued := jar.xsrf(t)
			if w := jar.send(t, before.Router, http.MethodPost, "/form", issued); w.Code != http.StatusOK {
				t.Fatalf("control: POST on the issuing instance = %d, want 200", w.Code)
			}

			after := csrfBagInstance(t, tt.store, records, false)
			if w := jar.send(t, after.Router, http.MethodPost, "/form", issued); w.Code != http.StatusOK {
				t.Fatalf("POST with the pre-restart token on a new instance = %d, want 200", w.Code)
			}
			if w := jar.send(t, after.Router, http.MethodPost, "/form", "garbage"); w.Code != contract419 {
				t.Fatalf("POST with a wrong token on the new instance = %d, want 419", w.Code)
			}
		})
	}
}

// TestCSRFToken_FollowsLoginAndLogout pins the session lifecycle on the
// token in the session: Login rotates it (the pre-login token stops
// validating, the XSRF-TOKEN the login response carries validates), and
// Logout invalidates it, on either session store.
func TestCSRFToken_FollowsLoginAndLogout(t *testing.T) {
	for _, store := range []string{"cookie", "server"} {
		t.Run(store, func(t *testing.T) {
			a := csrfBagInstance(t, store, nil, false)
			jar := csrfBagJar{}
			jar.send(t, a.Router, http.MethodGet, "/form", "")
			guest := jar.xsrf(t)

			if w := jar.send(t, a.Router, http.MethodPost, "/login", guest); w.Code != http.StatusOK {
				t.Fatalf("POST /login = %d", w.Code)
			}
			signedIn := jar.xsrf(t)
			if signedIn == guest {
				t.Fatal("login did not write a rotated XSRF-TOKEN")
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/form", guest); w.Code != contract419 {
				t.Fatalf("POST with the pre-login token = %d, want 419", w.Code)
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/form", signedIn); w.Code != http.StatusOK {
				t.Fatalf("POST with the post-login token = %d, want 200", w.Code)
			}

			cookieBeforeLogout := jar[a.config.Session.Name]
			if w := jar.send(t, a.Router, http.MethodPost, "/logout", signedIn); w.Code != http.StatusOK {
				t.Fatalf("POST /logout = %d", w.Code)
			}
			// A captured pre-logout cookie with its token must not pass.
			replay := csrfBagJar{a.config.Session.Name: cookieBeforeLogout}
			if w := replay.send(t, a.Router, http.MethodPost, "/form", signedIn); w.Code != contract419 {
				t.Fatalf("POST replaying the pre-logout session and token = %d, want 419", w.Code)
			}
		})
	}
}

// TestCSRFToken_SingleUseCannotBeReplayed pins single-use tokens on the
// token in the session: the first POST consumes the token, and replaying
// the same request (the same session cookie and token, which with the
// cookie store still carries the token) is refused on the instance.
func TestCSRFToken_SingleUseCannotBeReplayed(t *testing.T) {
	for _, store := range []string{"cookie", "server"} {
		t.Run(store, func(t *testing.T) {
			a := csrfBagInstance(t, store, nil, true)
			jar := csrfBagJar{}
			jar.send(t, a.Router, http.MethodGet, "/form", "")
			tok := jar.send(t, a.Router, http.MethodGet, "/token", "").Body.String()
			if tok == "" {
				t.Fatal("the page carries no CSRF token")
			}
			captured := csrfBagJar{}
			for k, v := range jar {
				captured[k] = v
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/form", tok); w.Code != http.StatusOK {
				t.Fatalf("first POST = %d, want 200", w.Code)
			}
			if w := captured.send(t, a.Router, http.MethodPost, "/form", tok); w.Code != contract419 {
				t.Fatalf("replayed POST with the captured cookie = %d, want 419", w.Code)
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/form", tok); w.Code != contract419 {
				t.Fatalf("replayed POST with the current cookie = %d, want 419", w.Code)
			}
		})
	}
}

const contract419 = 419
