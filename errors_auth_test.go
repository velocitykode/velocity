package velocity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// stubAuthUser is the user a stubAuthScheme authenticates.
type stubAuthUser struct{}

func (stubAuthUser) GetAuthIdentifier() interface{} { return "u-1" }
func (stubAuthUser) GetAuthPassword() string        { return "" }
func (stubAuthUser) GetRememberToken() string       { return "" }
func (stubAuthUser) SetRememberToken(string)        {}

// stubAuthScheme is a session-aware auth scheme with a fixed answer that
// counts user lookups.
type stubAuthScheme struct {
	authenticated bool
	sess          auth.Session

	mu      sync.Mutex
	lookups int
}

func (s *stubAuthScheme) Check(*http.Request) bool { return s.authenticated }

func (s *stubAuthScheme) User(*http.Request) auth.Authenticatable {
	s.mu.Lock()
	s.lookups++
	s.mu.Unlock()
	if !s.authenticated {
		return nil
	}
	return stubAuthUser{}
}

func (s *stubAuthScheme) ID(*http.Request) interface{} {
	s.mu.Lock()
	s.lookups++
	s.mu.Unlock()
	return nil
}

func (s *stubAuthScheme) lookupCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

func (s *stubAuthScheme) Session(*http.Request) auth.Session            { return s.sess }
func (*stubAuthScheme) SetUserStore(auth.UserStore)                     {}
func (*stubAuthScheme) Logout(http.ResponseWriter, *http.Request) error { return nil }
func (*stubAuthScheme) Login(http.ResponseWriter, *http.Request, auth.Authenticatable, ...bool) error {
	return nil
}
func (*stubAuthScheme) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (*stubAuthScheme) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}

// authRequest builds a request of kind: "json", "browser" or "inertia".
func authRequest(method, target, kind string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	switch kind {
	case "json":
		r.Header.Set("Accept", "application/json")
	case "browser":
		r.Header.Set("Accept", "text/html")
	case "inertia":
		r.Header.Set("X-Inertia", "true")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		r.Header.Set("Accept", "text/html, application/xhtml+xml")
	}
	return r
}

// TestAuthErrorRules_ThroughApp drives each auth denial through a real
// app: the auth guard, the router boundary, the bridge and the error
// handler built by New, with the default auth manager's login target
// configured and no error page.
func TestAuthErrorRules_ThroughApp(t *testing.T) {
	requireAuth := func(m *auth.Manager) router.MiddlewareFunc { return auth.AuthMiddleware(m) }
	requireAbility := func(m *auth.Manager) router.MiddlewareFunc { return auth.AuthorizeMiddleware(m, "fly") }
	guestOnly := func(m *auth.Manager) router.MiddlewareFunc { return auth.GuestMiddleware(m) }
	tests := []struct {
		name                string
		authenticated       bool
		mw                  func(*auth.Manager) router.MiddlewareFunc
		handlerErr          error
		kind                string
		target              string
		wantStatus          int
		wantType            string
		wantLocation        string
		wantInertiaLocation string
		wantDetail          string
		wantStash           string
	}{
		{name: "unauthenticated json", mw: requireAuth, kind: "json", target: "/api/user", wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent, wantDetail: "Unauthorized"},
		{name: "unauthenticated browser", mw: requireAuth, kind: "browser", target: "/settings?tab=2", wantStatus: http.StatusSeeOther, wantLocation: "/auth/sign-in", wantStash: "/settings?tab=2"},
		{name: "unauthenticated inertia", mw: requireAuth, kind: "inertia", target: "/settings", wantStatus: http.StatusSeeOther, wantLocation: "/auth/sign-in", wantStash: "/settings"},
		{name: "forbidden json", authenticated: true, mw: requireAbility, kind: "json", target: "/reports", wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Forbidden"},
		{name: "forbidden browser", authenticated: true, mw: requireAbility, kind: "browser", target: "/reports", wantStatus: http.StatusForbidden, wantType: "text/html"},
		{name: "forbidden inertia", authenticated: true, mw: requireAbility, kind: "inertia", target: "/reports", wantStatus: http.StatusConflict, wantInertiaLocation: "/reports"},
		{name: "guest json", authenticated: true, mw: guestOnly, kind: "json", target: "/login", wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Already authenticated."},
		{name: "guest browser", authenticated: true, mw: guestOnly, kind: "browser", target: "/login", wantStatus: http.StatusSeeOther, wantLocation: "/"},
		{name: "handler unauthenticated browser", handlerErr: &auth.UnauthenticatedError{}, kind: "browser", target: "/account", wantStatus: http.StatusSeeOther, wantLocation: "/auth/sign-in"},
		{name: "handler forbidden json", handlerErr: &auth.ForbiddenError{Err: errors.New("policy said no")}, kind: "json", target: "/account", wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec := newPipelineApp(t)
			a.Services.Errors.SetDebug(false)
			m := auth.FromServices(a.Services)
			if m == nil {
				t.Fatal("app has no *auth.Manager")
			}
			sess := auth.NewSession("sid")
			m.RegisterScheme("web", &stubAuthScheme{authenticated: tt.authenticated, sess: sess})
			m.SetLoginRedirect(func(*http.Request) string { return "/auth/sign-in" })
			if tt.mw != nil {
				a.Router.Use(tt.mw(m))
			}
			path := tt.target
			if i := strings.IndexByte(path, '?'); i >= 0 {
				path = path[:i]
			}
			a.Router.Get(path, func(*router.Context) error {
				if tt.handlerErr != nil {
					return tt.handlerErr
				}
				t.Error("guarded handler ran")
				return nil
			})

			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, authRequest(http.MethodGet, tt.target, tt.kind))

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); tt.wantType != "" && !strings.HasPrefix(got, tt.wantType) {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantType)
			}
			if got := w.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
			if got := w.Header().Get("X-Inertia-Location"); got != tt.wantInertiaLocation {
				t.Errorf("X-Inertia-Location = %q, want %q", got, tt.wantInertiaLocation)
			}
			if tt.wantDetail != "" {
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("decode problem body: %v (%q)", err, w.Body.String())
				}
				if body["detail"] != tt.wantDetail {
					t.Errorf("detail = %v, want %q", body["detail"], tt.wantDetail)
				}
			}
			if got, _ := sess.Get(router.IntendedSessionKey).(string); got != tt.wantStash {
				t.Errorf("stashed intended = %q, want %q", got, tt.wantStash)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0 (auth denials are not reported)", rec.count())
			}
		})
	}
}

// TestAuthErrorRules_ManagerResolvedPerRequest asserts the render rule
// reads the auth manager from the failed request's services, so a manager
// replaced after New decides the login target and no manager falls back
// to /login.
func TestAuthErrorRules_ManagerResolvedPerRequest(t *testing.T) {
	replaced := auth.NewManager()
	replaced.SetLoginRedirect(func(*http.Request) string { return "/replaced/login" })
	tests := []struct {
		name         string
		auth         contract.AuthManager
		wantLocation string
	}{
		{name: "replaced manager", auth: replaced, wantLocation: "/replaced/login"},
		{name: "no auth manager", auth: nil, wantLocation: "/login"},
		{name: "foreign auth manager", auth: stubUserAuth{id: "u-1"}, wantLocation: "/login"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec := newPipelineApp(t)
			a.Services.Auth = tt.auth
			a.Router.Get("/account", func(*router.Context) error { return &auth.UnauthenticatedError{} })

			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, authRequest(http.MethodGet, "/account", "browser"))

			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("response = %d %q, want 303 %q", w.Code, w.Header().Get("Location"), tt.wantLocation)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
		})
	}
}

// TestInstallAuthErrorRules asserts the rule on a bare handler: with no
// services on the request the default login target applies, a request
// that wants JSON gets the 401 problem body, and a user render rule for
// the same type outranks the framework's.
func TestInstallAuthErrorRules(t *testing.T) {
	tests := []struct {
		name         string
		kind         string
		userStatus   int
		wantStatus   int
		wantLocation string
	}{
		{name: "browser", kind: "browser", wantStatus: http.StatusSeeOther, wantLocation: "/login"},
		{name: "json", kind: "json", wantStatus: http.StatusUnauthorized},
		{name: "user rule wins", kind: "browser", userStatus: http.StatusTeapot, wantStatus: http.StatusTeapot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingReporter{}
			h := problem.NewHandler(problem.WithReporters(rec))
			installFrameworkErrorRules(h)
			if tt.userStatus != 0 {
				problem.RenderStatus[*auth.UnauthenticatedError](h, tt.userStatus)
			}
			w := httptest.NewRecorder()
			h.HandleRequest(contract.NewRenderContext(w, authRequest(http.MethodGet, "/x", tt.kind)), &auth.UnauthenticatedError{}, nil)

			if w.Code != tt.wantStatus || w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("response = %d %q, want %d %q", w.Code, w.Header().Get("Location"), tt.wantStatus, tt.wantLocation)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}
		})
	}
}

// countingUserStore is a user store that counts lookups and finds no one.
type countingUserStore struct {
	auth.UserStore

	mu      sync.Mutex
	lookups int
}

func (s *countingUserStore) FindByIDCtx(context.Context, interface{}) (auth.Authenticatable, error) {
	s.mu.Lock()
	s.lookups++
	s.mu.Unlock()
	return nil, nil
}

func (s *countingUserStore) FindByID(interface{}) (auth.Authenticatable, error) {
	return s.FindByIDCtx(context.Background(), nil)
}

func (s *countingUserStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookups
}

// TestErrorPipeline_RequestUserIDFromAuthManager asserts that the auth
// manager names the user of a failed request in its report from the
// session alone: no user lookup with a session, and none without one.
func TestErrorPipeline_RequestUserIDFromAuthManager(t *testing.T) {
	withUser := auth.NewSession("sid")
	withUser.Put("user_id", "u-9")
	stubWithUser := &stubAuthScheme{sess: withUser}
	stubNoUser := &stubAuthScheme{sess: auth.NewSession("sid")}

	enc, err := crypto.NewEncryptor(crypto.Config{Key: strings.Repeat("k", 32), Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	store := &countingUserStore{}
	sessionScheme, err := schemes.NewSessionScheme(store, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}

	tests := []struct {
		name    string
		scheme  auth.Scheme
		lookups func() int
		want    string
	}{
		{name: "session names the user", scheme: stubWithUser, lookups: stubWithUser.lookupCount, want: "u-9"},
		{name: "session without a user", scheme: stubNoUser, lookups: stubNoUser.lookupCount},
		{name: "session scheme without a cookie", scheme: sessionScheme, lookups: store.count},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec := newPipelineApp(t)
			auth.FromServices(a.Services).RegisterScheme("web", tt.scheme)
			a.Router.Get("/boom", func(*router.Context) error { return errors.New("boom") })

			a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

			if rec.count() != 1 {
				t.Fatalf("reports = %d, want 1", rec.count())
			}
			if got := rec.exCtx[0].UserID; got != tt.want {
				t.Errorf("UserID = %q, want %q", got, tt.want)
			}
			if n := tt.lookups(); n != 0 {
				t.Errorf("user lookups = %d, want 0", n)
			}
		})
	}
}

// TestAuthErrorRules_InertiaWithErrorPageReachesLogin asserts an
// unauthenticated Inertia visit is redirected to the login target even
// with an error component configured: the intended URL is stashed and the
// Error component is never rendered at 401.
func TestAuthErrorRules_InertiaWithErrorPageReachesLogin(t *testing.T) {
	a := newInertiaApp(t, "Error", false)
	m := auth.FromServices(a.Services)
	if m == nil {
		t.Fatal("app has no *auth.Manager")
	}
	sess := auth.NewSession("sid")
	m.RegisterScheme("web", &stubAuthScheme{sess: sess})
	m.SetLoginRedirect(func(*http.Request) string { return "/auth/sign-in" })
	a.Router.Get("/dashboard", func(*router.Context) error {
		t.Error("guarded handler ran")
		return nil
	}).Use(auth.AuthMiddleware(m))

	req := authRequest(http.MethodGet, "/dashboard?tab=1", "inertia")
	req.Header.Set("X-Inertia-Version", "v1")
	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/sign-in" {
		t.Fatalf("response = %d %q, want 303 /auth/sign-in (body %q)", w.Code, w.Header().Get("Location"), w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"component":"Error"`) {
		t.Errorf("error component rendered: %q", w.Body.String())
	}
	if got, _ := sess.Get(router.IntendedSessionKey).(string); got != "/dashboard?tab=1" {
		t.Errorf("stashed intended = %q, want /dashboard?tab=1", got)
	}
}
