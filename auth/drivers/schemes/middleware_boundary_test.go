package schemes

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// boundaryErr is the error the render-rule case maps to its own response.
type boundaryErr struct{}

func (boundaryErr) Error() string { return "boundary" }

// sessionFromCookies decrypts the session cookie among cookies and loads
// the session the next request presenting it would see, or fails.
func sessionFromCookies(t *testing.T, scheme *SessionScheme, cookies []*http.Cookie) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	found := false
	for _, c := range cookies {
		if c.Name == "vel_session" {
			req.AddCookie(c)
			found = true
		}
	}
	if !found {
		t.Fatalf("response carries no vel_session cookie (cookies %v)", cookies)
	}
	sess := scheme.Session(req)
	if sess == nil {
		t.Fatal("the session cookie did not load")
	}
	return map[string]any{"k": sess.Get("k"), "seen": sess.Get("seen")}
}

// serveOnce serves one GET / through h on a real server and returns the
// response.
func serveOnce(t *testing.T, h http.Handler) *http.Response {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// TestSessionMiddleware_SavesThroughErrorBoundary asserts the session is
// saved with the changes the error path makes after the middleware
// returned (a render rule or an installed error handler), and still saved
// when neither the handler nor the error path writes anything, through the
// router, through Wrap, and through a writer without the pre-commit hook.
func TestSessionMiddleware_SavesThroughErrorBoundary(t *testing.T) {
	putK := func(scheme *SessionScheme) router.HandlerFunc {
		return func(c *router.Context) error {
			scheme.Session(c.Request).Put("k", "v")
			return nil
		}
	}
	tests := []struct {
		name       string
		serve      func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie)
		wantStatus int
		wantK      any
		wantSeen   any
	}{
		{
			name: "pipeline render rule puts on error",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				h := problem.NewHandler(problem.WithReporters())
				problem.RenderFor(h, func(rc problem.RenderContext, _ boundaryErr, _ *problem.ErrorContext) bool {
					scheme.Session(rc.Request()).Put("seen", "yes")
					rc.WriteHeader(http.StatusTeapot)
					return true
				})
				r := router.New()
				routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
				r.Use(scheme.SessionMiddleware())
				r.Get("/", func(c *router.Context) error {
					scheme.Session(c.Request).Put("k", "v")
					return boundaryErr{}
				})
				resp := serveOnce(t, r)
				return resp.StatusCode, resp.Cookies()
			},
			wantStatus: http.StatusTeapot, wantK: "v", wantSeen: "yes",
		},
		{
			name: "installed error handler puts on error",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				r := router.New()
				r.SetErrorHandler(func(c *router.Context, _ error, _ router.ErrorInfo) {
					scheme.Session(c.Request).Put("seen", "yes")
					c.Response.WriteHeader(http.StatusInternalServerError)
				})
				r.Use(scheme.SessionMiddleware())
				r.Get("/", func(*router.Context) error { return errors.New("boom") })
				resp := serveOnce(t, r)
				return resp.StatusCode, resp.Cookies()
			},
			wantStatus: http.StatusInternalServerError, wantSeen: "yes",
		},
		{
			name: "router default error answer",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				r := router.New()
				r.Use(scheme.SessionMiddleware())
				r.Get("/", func(c *router.Context) error {
					scheme.Session(c.Request).Put("k", "v")
					return errors.New("boom")
				})
				resp := serveOnce(t, r)
				return resp.StatusCode, resp.Cookies()
			},
			wantStatus: http.StatusInternalServerError, wantK: "v",
		},
		{
			name: "router implicit 200",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				r := router.New()
				r.Use(scheme.SessionMiddleware())
				r.Get("/", putK(scheme))
				resp := serveOnce(t, r)
				return resp.StatusCode, resp.Cookies()
			},
			wantStatus: http.StatusOK, wantK: "v",
		},
		{
			name: "wrap implicit 200",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				resp := serveOnce(t, router.Wrap(scheme.SessionMiddleware()(putK(scheme))))
				return resp.StatusCode, resp.Cookies()
			},
			wantStatus: http.StatusOK, wantK: "v",
		},
		{
			name: "recorder without the hook saves after the handler",
			serve: func(t *testing.T, scheme *SessionScheme) (int, []*http.Cookie) {
				rec := httptest.NewRecorder()
				c := router.NewContext(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				if err := scheme.SessionMiddleware()(putK(scheme))(c); err != nil {
					t.Fatalf("handler: %v", err)
				}
				return rec.Code, rec.Result().Cookies()
			},
			wantStatus: http.StatusOK, wantK: "v",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := newRealCookieScheme(t)
			status, cookies := tt.serve(t, scheme)
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			got := sessionFromCookies(t, scheme, cookies)
			if got["k"] != tt.wantK || got["seen"] != tt.wantSeen {
				t.Errorf("next request's session k = %v, seen = %v; want %v, %v", got["k"], got["seen"], tt.wantK, tt.wantSeen)
			}
		})
	}
}
