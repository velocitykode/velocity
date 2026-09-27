package schemes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	csrfstores "github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// recallCSRFStack is the session middleware, the CSRF middleware wired the
// way velocity.New wires it (session resolver, session-bound bootstrap
// write) over a memory token store, and an auth-required page that renders
// the request's CSRF token.
func recallCSRFStack(t *testing.T) (*SessionScheme, csrf.Store, http.Handler) {
	t.Helper()
	scheme, _ := newRevokeScheme(t, nil)
	scheme.SetUserStore(&rememberRevivalStore{user: &revokeTestUser{id: "u1"}})

	cfg := csrf.DefaultConfig()
	cfg.CookiePolicy = contract.NewCookiePolicy("/", "", false, http.SameSiteLaxMode)
	cfg.Store = csrfstores.NewMemoryStore()
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		s, err := scheme.ResolveSession(r)
		if err != nil {
			return "", csrf.ErrNoSession
		}
		return s.ID(), nil
	}
	cfg.QueueAfterSessionSave = QueueSessionBoundWrite
	c, err := csrf.NewE(cfg)
	if err != nil {
		t.Fatalf("csrf.NewE: %v", err)
	}
	scheme.SetCSRFTokenRotator(c)

	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Use(c.RouterMiddleware())
	requireUser := func(next router.HandlerFunc) router.HandlerFunc {
		return func(ctx *router.Context) error {
			if scheme.User(ctx.Request) == nil {
				return ctx.String(http.StatusUnauthorized, "out")
			}
			return next(ctx)
		}
	}
	r.Get("/dashboard", requireUser(func(ctx *router.Context) error {
		tok, err := csrf.TokenForRequest(ctx.Request)
		if err != nil {
			return err
		}
		return ctx.String(http.StatusOK, tok)
	}))
	r.Post("/submit", requireUser(func(ctx *router.Context) error {
		return ctx.String(http.StatusOK, "accepted")
	}))
	return scheme, cfg.Store, r
}

// A request whose session cookie is gone but whose remember cookie is
// valid is revived by the auth-required page: the response carries one
// XSRF-TOKEN, the token the page renders, which the store holds for the
// revived session, and the next submit presenting it is accepted.
func TestCSRFRotation_RememberRevivalRendersTheRotatedToken(t *testing.T) {
	scheme, store, h := recallCSRFStack(t)
	remember := mintRememberCookie(t, scheme)

	req := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	req.AddCookie(remember)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /dashboard with the remember cookie: %d %s", w.Code, w.Body.String())
	}
	rendered := w.Body.String()

	var xsrf []string
	var sessionCookie, rotatedRemember *http.Cookie
	for _, line := range w.Header().Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		switch c.Name {
		case "XSRF-TOKEN":
			v, _ := url.QueryUnescape(c.Value)
			xsrf = append(xsrf, v)
		case "vel_session":
			sessionCookie = c
		case rememberCookieName:
			rotatedRemember = c
		}
	}
	if len(xsrf) != 1 {
		t.Fatalf("XSRF-TOKEN Set-Cookie lines = %d, want exactly 1", len(xsrf))
	}
	if xsrf[0] != rendered {
		t.Fatalf("XSRF-TOKEN cookie %q differs from the rendered token %q", xsrf[0], rendered)
	}
	if sessionCookie == nil || rotatedRemember == nil {
		t.Fatalf("the revival set session=%v remember=%v", sessionCookie, rotatedRemember)
	}

	newID := decryptSessionID(t, scheme.encryptor, sessionCookie.Value)
	stored, err := store.Get(context.Background(), newID)
	if err != nil || stored == "" {
		t.Fatalf("no token stored for the revived session %q: %v", newID, err)
	}
	if csrf.UnmaskToken(rendered) != stored {
		t.Fatal("the rendered token does not unmask to the token the store holds for the revived session")
	}

	post := httptest.NewRequest(http.MethodPost, "/submit", strings.NewReader(""))
	post.AddCookie(&http.Cookie{Name: "vel_session", Value: sessionCookie.Value})
	post.Header.Set("X-CSRF-Token", rendered)
	pw := httptest.NewRecorder()
	h.ServeHTTP(pw, post)
	if pw.Code != http.StatusOK {
		t.Fatalf("POST with the rendered token after the revival: %d %s, want 200", pw.Code, pw.Body.String())
	}
}
