package velocity

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/router"
)

// recallUser is a user whose remember token the recall rotates.
type recallUser struct {
	id    int
	token string
}

func (u *recallUser) GetAuthIdentifier() interface{} { return u.id }
func (u *recallUser) GetAuthPassword() string        { return "" }
func (u *recallUser) GetRememberToken() string       { return u.token }
func (u *recallUser) SetRememberToken(t string)      { u.token = t }

// recallUserStore keeps the one test user in memory and supports the
// compare-and-swap a remember-me recall needs.
type recallUserStore struct {
	auth.UserStore
	mu   sync.Mutex
	user *recallUser
}

func (s *recallUserStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fmt.Sprint(id) == fmt.Sprint(s.user.id) {
		return &recallUser{id: s.user.id, token: s.user.token}, nil
	}
	return nil, auth.ErrUserNotFound
}

func (s *recallUserStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	return s.FindByIDCtx(context.Background(), id)
}

func (s *recallUserStore) UpdateRememberTokenCtx(_ context.Context, u auth.Authenticatable, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user.token = token
	u.SetRememberToken(token)
	return nil
}

func (s *recallUserStore) UpdateRememberToken(u auth.Authenticatable, token string) error {
	return s.UpdateRememberTokenCtx(context.Background(), u, token)
}

func (s *recallUserStore) CompareAndSwapRememberToken(_ context.Context, u auth.Authenticatable, oldToken, newToken string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.user.token != oldToken {
		return false, nil
	}
	s.user.token = newToken
	u.SetRememberToken(newToken)
	return true, nil
}

// xsrfLines returns the XSRF-TOKEN Set-Cookie values of a response,
// URL-decoded.
func xsrfLines(t *testing.T, h http.Header) []string {
	t.Helper()
	var out []string
	for _, line := range h.Values("Set-Cookie") {
		if !strings.HasPrefix(line, "XSRF-TOKEN=") {
			continue
		}
		c, err := http.ParseSetCookie(line)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		v, err := url.QueryUnescape(c.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

// recallApp is the bootstrapped app of csrfBagInstance with a user store
// that supports remember-me recall, an auth-required page that renders the
// request's CSRF token, and a GET that signs in and renders in the same
// request.
func recallApp(t *testing.T, sessionStore string) *App {
	t.Helper()
	a := csrfBagInstance(t, sessionStore, nil, false)
	m := auth.FromServices(a.Services)
	m.SetUserStore(&recallUserStore{user: &recallUser{id: 9}})
	render := func(c *router.Context) error {
		tok, err := csrf.TokenForRequest(c.Request)
		if err != nil {
			return err
		}
		return c.String(http.StatusOK, tok)
	}
	a.Router.Post("/login-remember", func(c *router.Context) error {
		if err := m.Login(c.Response, c.Request, &recallUser{id: 9}, true); err != nil {
			return err
		}
		return c.String(http.StatusOK, "in")
	})
	a.Router.Group("", func(g router.Router) {
		g.Use(auth.AuthMiddleware(m))
		g.Get("/dashboard", render)
	})
	a.Router.Get("/login-render", func(c *router.Context) error {
		if err := m.Login(c.Response, c.Request, &recallUser{id: 9}); err != nil {
			return err
		}
		return render(c)
	})
	return a
}

// A remember-me revival renders the rotated CSRF token: the response
// carries exactly one XSRF-TOKEN, equal to the token the page renders, and
// the next submit presenting that token with the session the response set
// is accepted.
func TestCSRFToken_RememberRevivalRendersTheRotatedToken(t *testing.T) {
	for _, store := range []string{"cookie", "server"} {
		t.Run(store, func(t *testing.T) {
			a := recallApp(t, store)
			jar := csrfBagJar{}
			if w := jar.send(t, a.Router, http.MethodGet, "/form", ""); w.Code != http.StatusOK {
				t.Fatalf("GET /form: %d", w.Code)
			}
			if w := jar.send(t, a.Router, http.MethodPost, "/login-remember", jar.xsrf(t)); w.Code != http.StatusOK {
				t.Fatalf("POST /login-remember: %d %s", w.Code, w.Body.String())
			}
			if _, ok := jar["remember_"+a.config.Session.Name]; !ok {
				t.Fatal("no remember cookie after a remember-me sign-in")
			}
			// The session ends (cookie gone); the remember cookie stays.
			delete(jar, a.config.Session.Name)
			delete(jar, "XSRF-TOKEN")

			w := jar.send(t, a.Router, http.MethodGet, "/dashboard", "")
			if w.Code != http.StatusOK {
				t.Fatalf("GET /dashboard after the session ended: %d %s", w.Code, w.Body.String())
			}
			rendered := w.Body.String()
			if rendered == "" {
				t.Fatal("the revived page rendered no CSRF token")
			}
			lines := xsrfLines(t, w.Header())
			if len(lines) != 1 {
				t.Fatalf("XSRF-TOKEN Set-Cookie lines = %d (%q), want exactly 1", len(lines), lines)
			}
			if lines[0] != rendered {
				t.Fatalf("XSRF-TOKEN cookie %q differs from the rendered token %q", lines[0], rendered)
			}
			if _, ok := jar[a.config.Session.Name]; !ok {
				t.Fatal("the revival set no session cookie")
			}

			if w := jar.send(t, a.Router, http.MethodPost, "/form", rendered); w.Code != http.StatusOK {
				t.Fatalf("POST with the rendered token after the revival: %d %s, want 200", w.Code, w.Body.String())
			}
		})
	}
}

// A sign-in that renders in the same request renders the rotated token,
// and the response carries it as its one XSRF-TOKEN.
func TestCSRFToken_SignInRenderingInTheSameRequestRendersTheRotatedToken(t *testing.T) {
	a := recallApp(t, "cookie")
	jar := csrfBagJar{}
	jar.send(t, a.Router, http.MethodGet, "/form", "")

	w := jar.send(t, a.Router, http.MethodGet, "/login-render", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /login-render: %d %s", w.Code, w.Body.String())
	}
	rendered := w.Body.String()
	lines := xsrfLines(t, w.Header())
	if len(lines) != 1 || lines[0] != rendered {
		t.Fatalf("XSRF-TOKEN lines = %q, want exactly the rendered token %q", lines, rendered)
	}
	if w := jar.send(t, a.Router, http.MethodPost, "/form", rendered); w.Code != http.StatusOK {
		t.Fatalf("POST with the token the sign-in rendered: %d, want 200", w.Code)
	}
}

// The safe-method bootstrap write still lands once for a plain anonymous
// GET and for a signed-in GET with no transition.
func TestCSRFToken_BootstrapWriteLandsOnceWithoutATransition(t *testing.T) {
	a := recallApp(t, "cookie")
	jar := csrfBagJar{}
	w := jar.send(t, a.Router, http.MethodGet, "/token", "")
	if lines := xsrfLines(t, w.Header()); len(lines) != 1 || lines[0] != w.Body.String() {
		t.Fatalf("anonymous GET: XSRF-TOKEN lines = %q, want exactly the rendered %q", lines, w.Body.String())
	}
	if w := jar.send(t, a.Router, http.MethodPost, "/login", jar.xsrf(t)); w.Code != http.StatusOK {
		t.Fatalf("POST /login: %d", w.Code)
	}
	w = jar.send(t, a.Router, http.MethodGet, "/token", "")
	if lines := xsrfLines(t, w.Header()); len(lines) != 1 || lines[0] != w.Body.String() {
		t.Fatalf("signed-in GET: XSRF-TOKEN lines = %q, want exactly the rendered %q", lines, w.Body.String())
	}
}
