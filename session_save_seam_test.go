package velocity

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

type saveSeamUser struct{ id int }

func (u *saveSeamUser) GetAuthIdentifier() interface{} { return u.id }
func (u *saveSeamUser) GetAuthPassword() string        { return "" }
func (u *saveSeamUser) GetRememberToken() string       { return "" }
func (u *saveSeamUser) SetRememberToken(string)        {}

// saveSeamUserStore finds the one test user by id, so an authenticated
// request after login proves the login's session was saved.
type saveSeamUserStore struct {
	auth.UserStore
	user *saveSeamUser
}

func (s *saveSeamUserStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	if fmt.Sprint(id) == fmt.Sprint(s.user.id) {
		return s.user, nil
	}
	return nil, nil
}

func (s *saveSeamUserStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	return s.FindByIDCtx(context.Background(), id)
}

func (s *saveSeamUserStore) UpdateRememberTokenCtx(context.Context, auth.Authenticatable, string) error {
	return nil
}

// saveSeamServer boots a real app the way a starter template does
// (ConfigFromEnv, AUTH_SCHEME=web) with a view engine, bootstrapped or in
// embed mode (New only), and serves routes that exercise every framework
// path that used to save the session on its own.
func saveSeamServer(t *testing.T, bootstrap bool) (*httptest.Server, *http.Client) {
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
	cfg := ConfigFromEnv()
	cfg.View = view.Config{RootTemplate: inertiaTestTemplate, Version: "v1"}
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	if bootstrap {
		if err := a.Bootstrap(); err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
	}
	m := auth.FromServices(a.Services)
	user := &saveSeamUser{id: 7}
	scheme, err := m.DefaultScheme()
	if err != nil {
		t.Fatalf("DefaultScheme: %v", err)
	}
	scheme.(*schemes.SessionScheme).SetUserStore(&saveSeamUserStore{user: user})
	a.Router.Get("/dashboard", func(c *router.Context) error {
		return c.String(http.StatusOK, "dashboard")
	}).Use(auth.AuthMiddleware(m))
	a.Router.Post("/login", func(c *router.Context) error {
		if err := m.Login(c.Response, c.Request, user); err != nil {
			return err
		}
		return c.RedirectToIntended("/home")
	})
	a.Router.Post("/logout", func(c *router.Context) error {
		if err := m.Logout(c.Response, c.Request); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/")
	})
	a.Router.Post("/flash", func(c *router.Context) error {
		view.For(c).Flash("status", "saved").Redirect("/after-flash")
		return nil
	})
	a.Router.Get("/after-flash", func(c *router.Context) error {
		status, _ := m.Session(c.Request).GetFlash("status").(string)
		return c.String(http.StatusOK, status)
	})

	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return srv, client
}

// saveSeamDo sends one request and returns the response with its body
// read, plus the positions of its session and XSRF-TOKEN Set-Cookie
// headers.
func saveSeamDo(t *testing.T, client *http.Client, method, u string) (resp *http.Response, body string, session []int, xsrf int) {
	t.Helper()
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	if _, err := io.Copy(&b, resp.Body); err != nil {
		t.Fatalf("%s %s: read body: %v", method, u, err)
	}
	xsrf = -1
	for i, raw := range resp.Header.Values("Set-Cookie") {
		switch {
		case strings.HasPrefix(raw, "velocity_session="):
			session = append(session, i)
		case strings.HasPrefix(raw, "XSRF-TOKEN="):
			xsrf = i
		}
	}
	return resp, b.String(), session, xsrf
}

// Every response that changes the session carries exactly one session
// Set-Cookie, written by the session middleware, in a bootstrapped app and
// in embed mode alike: the unauthenticated stash of the intended URL,
// login followed by RedirectToIntended (with the XSRF-TOKEN cookie after
// the saved session), a view flash followed by a redirect, and logout.
func TestSessionSave_OneSessionCookiePerResponse(t *testing.T) {
	for _, mode := range []struct {
		name      string
		bootstrap bool
	}{
		{"bootstrapped", true},
		{"embed mode", false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			srv, client := saveSeamServer(t, mode.bootstrap)

			steps := []struct {
				name, method, path string
				wantStatus         int
				wantLocation       string
				wantBody           string
				wantXSRF           bool
			}{
				{name: "unauthenticated stash", method: http.MethodGet, path: "/dashboard", wantStatus: http.StatusSeeOther, wantLocation: "/login"},
				{name: "login then redirect to intended", method: http.MethodPost, path: "/login", wantStatus: http.StatusSeeOther, wantLocation: "/dashboard", wantXSRF: true},
				{name: "authenticated after login", method: http.MethodGet, path: "/dashboard", wantStatus: http.StatusOK, wantBody: "dashboard"},
				{name: "flash then redirect", method: http.MethodPost, path: "/flash", wantStatus: http.StatusSeeOther, wantLocation: "/after-flash"},
				{name: "flash reaches the next request", method: http.MethodGet, path: "/after-flash", wantStatus: http.StatusOK, wantBody: "saved"},
				{name: "logout", method: http.MethodPost, path: "/logout", wantStatus: http.StatusSeeOther, wantLocation: "/"},
				{name: "guest after logout", method: http.MethodGet, path: "/dashboard", wantStatus: http.StatusSeeOther, wantLocation: "/login"},
			}
			for _, st := range steps {
				resp, body, session, xsrf := saveSeamDo(t, client, st.method, srv.URL+st.path)
				if resp.StatusCode != st.wantStatus {
					t.Fatalf("%s: status %d, want %d", st.name, resp.StatusCode, st.wantStatus)
				}
				if loc := resp.Header.Get("Location"); loc != st.wantLocation {
					t.Fatalf("%s: Location %q, want %q", st.name, loc, st.wantLocation)
				}
				if st.wantBody != "" && body != st.wantBody {
					t.Errorf("%s: body %q, want %q", st.name, body, st.wantBody)
				}
				if st.wantLocation != "" && len(session) != 1 {
					t.Errorf("%s: %d velocity_session Set-Cookie headers, want 1", st.name, len(session))
				}
				if st.wantXSRF {
					if xsrf < 0 {
						t.Errorf("%s: no XSRF-TOKEN cookie", st.name)
					} else if len(session) > 0 && xsrf < session[0] {
						t.Errorf("%s: XSRF-TOKEN written before the session was saved", st.name)
					}
				}
			}
		})
	}
}

// No framework code saves a session outside the session middleware's
// seam (commitSession through saveSessionFromMiddleware), so one request
// never emits two session cookies.
func TestSessionSave_OnlyTheSeamSavesSessions(t *testing.T) {
	const seamFile = "auth/drivers/schemes/middleware.go"
	const seamFunc = "saveSessionFromMiddleware"
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		seam := filepath.ToSlash(path) == seamFile
		ast.Inspect(f, func(n ast.Node) bool {
			if seam {
				if decl, ok := n.(*ast.FuncDecl); ok && decl.Name.Name == seamFunc {
					return false
				}
				if vs, ok := n.(*ast.ValueSpec); ok && len(vs.Names) == 1 && vs.Names[0].Name == seamFunc {
					return false
				}
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Save" {
				offenders = append(offenders, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Errorf("session saved outside the session middleware's seam:\n  %s", strings.Join(offenders, "\n  "))
	}
}
