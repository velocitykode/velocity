package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/router"
)

// csrfResolverApp boots a real app the way a starter template does
// (ConfigFromEnv, AUTH_SCHEME=web, 120 minute session lifetime), without
// Bootstrap: embed mode.
func csrfResolverApp(t *testing.T) *App {
	t.Helper()
	for k, v := range map[string]string{
		"APP_ENV":          "local",
		"APP_KEY":          strings.Repeat("k", 32),
		"AUTH_SCHEME":      "web",
		"LOG_DRIVER":       "null",
		"CACHE_DRIVER":     "memory",
		"QUEUE_DRIVER":     "memory",
		"MAIL_DRIVER":      "log",
		"SESSION_SECURE":   "false",
		"SESSION_LIFETIME": "120",
	} {
		t.Setenv(k, v)
	}
	a, err := New(WithConfig(ConfigFromEnv()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a
}

// csrfResolverLogin logs a user in through the real session scheme and
// returns the session cookie the store saved for it.
func csrfResolverLogin(t *testing.T, a *App) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	if err := auth.FromServices(a.Services).Login(w, r, &saveSeamUser{id: 9}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == a.config.Session.Name {
			return &http.Cookie{Name: c.Name, Value: c.Value}
		}
	}
	t.Fatal("login wrote no session cookie")
	return nil
}

// csrfResolverExpire returns a copy of a real session cookie whose issue
// and creation times are three hours old, past the 120 minute lifetime,
// so the session store rejects it while the id inside stays the same.
func csrfResolverExpire(t *testing.T, a *App, c *http.Cookie) *http.Cookie {
	t.Helper()
	plaintext, err := a.Crypto.Decrypt(c.Value)
	if err != nil {
		t.Fatalf("decrypt session cookie: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		t.Fatalf("decode session cookie: %v", err)
	}
	old := time.Now().Add(-3 * time.Hour)
	payload["iat"], payload["cat"] = old, old
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := a.Crypto.Encrypt(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: c.Name, Value: enc}
}

func csrfResolverRequest(method string, cookie *http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "/form", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return r
}

// TestCSRFSessionResolver_RejectsSessionsTheStoreRejects pins that the
// resolver New installs keys CSRF tokens only by a session the session
// store accepts. It is reached on its own (no session middleware in
// front) whenever the CSRF middleware is mounted outside a.Router, or
// the resolver is called directly.
func TestCSRFSessionResolver_RejectsSessionsTheStoreRejects(t *testing.T) {
	t.Run("valid cookie resolves to its session id", func(t *testing.T) {
		a := csrfResolverApp(t)
		cookie := csrfResolverLogin(t, a)
		r := csrfResolverRequest(http.MethodGet, cookie)
		want := auth.FromServices(a.Services).Session(r).ID()
		got, err := a.config.CSRF.SessionIDResolver(csrfResolverRequest(http.MethodGet, cookie))
		if err != nil || got != want {
			t.Fatalf("resolver = (%q, %v), want (%q, nil)", got, err, want)
		}
	})

	t.Run("expired cookie", func(t *testing.T) {
		a := csrfResolverApp(t)
		live := csrfResolverLogin(t, a)
		liveID := auth.FromServices(a.Services).Session(csrfResolverRequest(http.MethodGet, live)).ID()
		expired := csrfResolverExpire(t, a, live)
		if id := auth.FromServices(a.Services).Session(csrfResolverRequest(http.MethodGet, expired)).ID(); id == liveID {
			t.Fatal("session store accepted the expired cookie; the test premise no longer holds")
		}
		got, err := a.config.CSRF.SessionIDResolver(csrfResolverRequest(http.MethodGet, expired))
		if got == liveID {
			t.Fatalf("resolver keyed the request to %q, a session the store rejects as expired", got)
		}
		if !errors.Is(err, csrf.ErrNoSession) {
			t.Fatalf("resolver = (%q, %v), want ErrNoSession", got, err)
		}
	})

	t.Run("cookie revoked at logout", func(t *testing.T) {
		a := csrfResolverApp(t)
		m := auth.FromServices(a.Services)
		cookie := csrfResolverLogin(t, a)
		loginID := m.Session(csrfResolverRequest(http.MethodGet, cookie)).ID()
		if err := m.Logout(httptest.NewRecorder(), csrfResolverRequest(http.MethodPost, cookie)); err != nil {
			t.Fatalf("Logout: %v", err)
		}
		if id := m.Session(csrfResolverRequest(http.MethodGet, cookie)).ID(); id == loginID {
			t.Fatal("session store accepted the revoked cookie; the test premise no longer holds")
		}
		got, err := a.config.CSRF.SessionIDResolver(csrfResolverRequest(http.MethodGet, cookie))
		if got == loginID {
			t.Fatalf("resolver keyed the request to %q, a session the store revoked at logout", got)
		}
		if !errors.Is(err, csrf.ErrNoSession) {
			t.Fatalf("resolver = (%q, %v), want ErrNoSession", got, err)
		}
	})

	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{"no cookie", nil},
		{"empty cookie", &http.Cookie{Name: "velocity_session", Value: ""}},
		{"undecryptable cookie", &http.Cookie{Name: "velocity_session", Value: "not-a-real-payload"}},
	} {
		t.Run(tc.name+" mints no ephemeral session", func(t *testing.T) {
			a := csrfResolverApp(t)
			got, err := a.config.CSRF.SessionIDResolver(csrfResolverRequest(http.MethodPost, tc.cookie))
			if got != "" || !errors.Is(err, csrf.ErrNoSession) {
				t.Fatalf("resolver = (%q, %v), want (\"\", ErrNoSession)", got, err)
			}
		})
	}
}

// csrfResolverFlow mints a CSRF token through the given handler with a
// live session cookie, then replays a POST carrying that token, once
// with the live cookie and once with the same session expired. The live
// POST must pass and the expired one must get 419.
func csrfResolverFlow(t *testing.T, a *App, h http.Handler) {
	t.Helper()
	live := csrfResolverLogin(t, a)
	gw := httptest.NewRecorder()
	h.ServeHTTP(gw, csrfResolverRequest(http.MethodGet, live))
	var xsrf string
	for _, c := range gw.Result().Cookies() {
		if c.Name == "XSRF-TOKEN" {
			xsrf = c.Value
		}
	}
	if xsrf == "" {
		t.Fatal("GET with a live session minted no XSRF-TOKEN")
	}
	token, err := url.QueryUnescape(xsrf)
	if err != nil {
		t.Fatal(err)
	}
	post := func(cookie *http.Cookie) int {
		r := csrfResolverRequest(http.MethodPost, cookie)
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := post(live); code != http.StatusOK {
		t.Fatalf("POST with the live session and its token = %d, want 200", code)
	}
	if code := post(csrfResolverExpire(t, a, live)); code != 419 {
		t.Fatalf("POST replaying the expired session cookie with its token = %d, want 419", code)
	}
}

// TestCSRFSessionResolver_ExpiredSessionPostGets419 drives the whole CSRF
// flow with a token minted for a live session and replayed after the
// session expired, with the CSRF middleware on a.Router in embed mode and
// in a bootstrapped app. The token lives in the session, so the CSRF
// middleware must run inside the session middleware New installs on
// a.Router; mounted outside it, it issues and accepts no token.
func TestCSRFSessionResolver_ExpiredSessionPostGets419(t *testing.T) {
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	t.Run("embed mode, csrf middleware on the router", func(t *testing.T) {
		a := csrfResolverApp(t)
		a.Router.Use(a.Services.CSRF.(*csrf.CSRF).RouterMiddleware())
		h := func(c *router.Context) error { return c.String(http.StatusOK, "ok") }
		a.Router.Get("/form", h)
		a.Router.Post("/form", h)
		csrfResolverFlow(t, a, a.Router)
	})

	t.Run("csrf middleware outside the session middleware fails closed", func(t *testing.T) {
		a := csrfResolverApp(t)
		mux := http.NewServeMux()
		mux.Handle("/form", a.Services.CSRF.(*csrf.CSRF).Middleware(http.HandlerFunc(ok)))
		lw := httptest.NewRecorder()
		if err := auth.FromServices(a.Services).Login(lw, httptest.NewRequest(http.MethodPost, "/login", nil), &saveSeamUser{id: 9}); err != nil {
			t.Fatalf("Login: %v", err)
		}
		var live *http.Cookie
		var token string
		for _, c := range lw.Result().Cookies() {
			switch c.Name {
			case a.config.Session.Name:
				live = &http.Cookie{Name: c.Name, Value: c.Value}
			case "XSRF-TOKEN":
				token, _ = url.QueryUnescape(c.Value)
			}
		}
		if live == nil || token == "" {
			t.Fatal("login wrote no session cookie or no XSRF-TOKEN")
		}
		gw := httptest.NewRecorder()
		mux.ServeHTTP(gw, csrfResolverRequest(http.MethodGet, live))
		for _, c := range gw.Result().Cookies() {
			if c.Name == "XSRF-TOKEN" {
				t.Fatal("GET outside the session middleware minted an XSRF-TOKEN")
			}
		}
		r := csrfResolverRequest(http.MethodPost, live)
		r.Header.Set("X-CSRF-Token", token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 419 {
			t.Fatalf("POST outside the session middleware with the session's token = %d, want 419", w.Code)
		}
	})

	t.Run("bootstrapped, csrf middleware on the router", func(t *testing.T) {
		a := csrfResolverApp(t)
		if err := a.Bootstrap(); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		a.Router.Use(a.Services.CSRF.(*csrf.CSRF).RouterMiddleware())
		h := func(c *router.Context) error { return c.String(http.StatusOK, "ok") }
		a.Router.Get("/form", h)
		a.Router.Post("/form", h)
		csrfResolverFlow(t, a, a.Router)
	})
}
