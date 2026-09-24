package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
)

// hasRole is a role checker over mockUser.roles.
func hasRole(u Authenticatable, role string) bool {
	mu, ok := u.(*mockUser)
	if !ok {
		return false
	}
	for _, r := range mu.roles {
		if r == role {
			return true
		}
	}
	return false
}

// denialFixture builds the manager and the middleware under test for one
// denial case.
type denialFixture func() (*Manager, router.MiddlewareFunc)

// unauthenticatedWith returns a fixture whose manager has no
// authenticated user, guarded by the middleware mw builds.
func unauthenticatedWith(mw func(*Manager) router.MiddlewareFunc) denialFixture {
	return func() (*Manager, router.MiddlewareFunc) {
		m := newManagerWithScheme(false)
		return m, mw(m)
	}
}

// signedInAs returns a fixture whose manager authenticates a user holding
// roles, guarded by the middleware mw builds.
func signedInAs(roles []string, mw func(*Manager) router.MiddlewareFunc) denialFixture {
	return func() (*Manager, router.MiddlewareFunc) {
		m := newManagerWithUser(&mockUser{id: 1, roles: roles})
		m.Access().SetRoleChecker(hasRole)
		return m, mw(m)
	}
}

func requireAuth(m *Manager) router.MiddlewareFunc { return AuthMiddleware(m) }
func requireAdmin(m *Manager) router.MiddlewareFunc {
	return RequireRole(m, "admin")
}
func requireAdminOrEditor(m *Manager) router.MiddlewareFunc {
	return RequireAnyRole(m, "admin", "editor")
}
func requireAdminAndEditor(m *Manager) router.MiddlewareFunc {
	return RequireAllRoles(m, "admin", "editor")
}
func requireAbility(ability string, allow bool) func(*Manager) router.MiddlewareFunc {
	return func(m *Manager) router.MiddlewareFunc {
		m.Access().Define(ability, func(Authenticatable, ...interface{}) bool { return allow })
		return AuthorizeMiddleware(m, ability)
	}
}
func requirePostOwner(m *Manager) router.MiddlewareFunc {
	m.Access().Define("edit-post", func(u Authenticatable, args ...interface{}) bool {
		owner, ok := args[0].(int)
		return ok && u.GetAuthIdentifier() == owner
	})
	return AuthorizeMiddleware(m, "edit-post", func(*router.Context) interface{} { return 999 })
}
func requireUndefinedAbility(m *Manager) router.MiddlewareFunc {
	return AuthorizeMiddleware(m, "fly")
}
func guestOnly(m *Manager) router.MiddlewareFunc { return GuestMiddleware(m) }
func guestOnlyToDashboard(m *Manager) router.MiddlewareFunc {
	return GuestMiddlewareWithRedirect(m, "/dashboard")
}

// writeTracker records whether anything reached the response writer.
type writeTracker struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (w *writeTracker) WriteHeader(code int) {
	w.wrote = true
	w.ResponseRecorder.WriteHeader(code)
}

func (w *writeTracker) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseRecorder.Write(p)
}

// countingReporter counts reported errors.
type countingReporter struct {
	mu sync.Mutex
	n  int
}

func (r *countingReporter) Report(error, *contract.ErrorContext) {
	r.mu.Lock()
	r.n++
	r.mu.Unlock()
}

func (r *countingReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Request kinds for newDenialRequest.
const (
	kindJSON    = "json"
	kindBrowser = "browser"
	kindInertia = "inertia"
	kindXHR     = "xhr"
)

// newDenialRequest builds a request of the given kind.
func newDenialRequest(method, target, kind string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	switch kind {
	case kindJSON:
		r.Header.Set("Accept", "application/json")
	case kindBrowser:
		r.Header.Set("Accept", "text/html")
	case kindInertia:
		r.Header.Set("X-Inertia", "true")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		r.Header.Set("Accept", "text/html, application/xhtml+xml")
	case kindXHR:
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	return r
}

// servePipeline serves req through a router whose error boundary runs the
// error pipeline, with auth's render rule bound to m the way the framework
// installs it, and mw guarding a handler that must not run.
func servePipeline(t *testing.T, m *Manager, mw router.MiddlewareFunc, req *http.Request) (*httptest.ResponseRecorder, *countingReporter) {
	t.Helper()
	rep := &countingReporter{}
	h := problem.NewHandler(problem.WithReporters(rep))
	h.AddFrameworkRenderRule(contract.RenderRule{
		Match: func(err error) bool {
			var ue *UnauthenticatedError
			return errors.As(err, &ue)
		},
		Render: m.RenderUnauthenticated,
	})
	r := router.New()
	routerbridge.Install(r, routerbridge.WithHandler(func() contract.ErrorHandler { return h }))
	r.Use(mw)
	r.Any(req.URL.Path, func(*router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, rep
}

// TestMiddleware_DenialReturnsTypedError asserts every guard returns the
// typed auth error for a denial and writes nothing itself.
func TestMiddleware_DenialReturnsTypedError(t *testing.T) {
	tests := []struct {
		name          string
		fixture       denialFixture
		method        string
		wantForbidden bool
	}{
		{name: "auth unauthenticated", fixture: unauthenticatedWith(requireAuth)},
		{name: "role unauthenticated", fixture: unauthenticatedWith(requireAdmin)},
		{name: "role missing", fixture: signedInAs([]string{"editor"}, requireAdmin), wantForbidden: true},
		{name: "any role unauthenticated", fixture: unauthenticatedWith(requireAdminOrEditor)},
		{name: "any role missing", fixture: signedInAs([]string{"viewer"}, requireAdminOrEditor), wantForbidden: true},
		{name: "all roles unauthenticated", fixture: unauthenticatedWith(requireAdminAndEditor)},
		{name: "all roles one missing", fixture: signedInAs([]string{"admin"}, requireAdminAndEditor), wantForbidden: true},
		{name: "ability unauthenticated", fixture: unauthenticatedWith(requireAbility("view-reports", true))},
		{name: "ability denied", fixture: signedInAs(nil, requireAbility("view-reports", false)), wantForbidden: true},
		{name: "resource ability denied", fixture: signedInAs(nil, requirePostOwner), method: http.MethodPut, wantForbidden: true},
		{name: "undefined ability", fixture: signedInAs(nil, requireUndefinedAbility), wantForbidden: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, mw := tt.fixture()
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			c := router.NewContext(w, newDenialRequest(method, "/guarded", kindBrowser))
			err := mw(func(*router.Context) error {
				t.Error("next handler should not be called")
				return nil
			})(c)

			if w.wrote || w.Header().Get("Location") != "" {
				t.Errorf("middleware wrote a response (code %d, Location %q), want nothing written", w.Code, w.Header().Get("Location"))
			}
			var rep contract.Reportable
			if !errors.As(err, &rep) || rep.ShouldReport() {
				t.Errorf("error %v must be Reportable with ShouldReport false", err)
			}
			status, _, _ := contract.StatusOf(err)
			if tt.wantForbidden {
				var fe *ForbiddenError
				if !errors.As(err, &fe) {
					t.Fatalf("error = %v, want *ForbiddenError", err)
				}
				if !errors.Is(err, ErrUnauthorized) {
					t.Error("errors.Is(err, ErrUnauthorized) = false, want true")
				}
				if status != http.StatusForbidden {
					t.Errorf("status = %d, want 403", status)
				}
				return
			}
			var ue *UnauthenticatedError
			if !errors.As(err, &ue) {
				t.Fatalf("error = %v, want *UnauthenticatedError", err)
			}
			if len(ue.Schemes) != 1 || ue.Schemes[0] != "web" {
				t.Errorf("Schemes = %v, want [web]", ue.Schemes)
			}
			if ue.RedirectTo != "/login" {
				t.Errorf("RedirectTo = %q, want /login", ue.RedirectTo)
			}
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", status)
			}
		})
	}
}

// TestMiddleware_DenialThroughPipeline asserts how the error pipeline
// answers each denial per request kind, and that none is reported.
func TestMiddleware_DenialThroughPipeline(t *testing.T) {
	signInAt := func(target string) denialFixture {
		return func() (*Manager, router.MiddlewareFunc) {
			m := newManagerWithScheme(false)
			m.SetLoginRedirect(func(*http.Request) string { return target })
			return m, AuthMiddleware(m)
		}
	}
	tests := []struct {
		name                string
		fixture             denialFixture
		method              string
		target              string
		kind                string
		wantStatus          int
		wantType            string
		wantLocation        string
		wantInertiaLocation string
		wantDetail          string
	}{
		{name: "unauthenticated json", fixture: unauthenticatedWith(requireAuth), target: "/api/user", kind: kindJSON, wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent, wantDetail: "Unauthorized"},
		{name: "unauthenticated xhr", fixture: unauthenticatedWith(requireAuth), target: "/api/user", kind: kindXHR, wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent, wantDetail: "Unauthorized"},
		{name: "unauthenticated browser", fixture: unauthenticatedWith(requireAuth), target: "/dashboard", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/login"},
		{name: "unauthenticated browser post", fixture: unauthenticatedWith(requireAuth), method: http.MethodPost, target: "/posts", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/login"},
		{name: "unauthenticated browser custom login", fixture: signInAt("/auth/sign-in"), target: "/dashboard", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/auth/sign-in"},
		{name: "unauthenticated browser unsafe login", fixture: signInAt("https://evil.example/login"), target: "/dashboard", kind: kindBrowser, wantStatus: http.StatusUnauthorized, wantType: "text/html"},
		{name: "unauthenticated inertia", fixture: unauthenticatedWith(requireAuth), target: "/dashboard?tab=1", kind: kindInertia, wantStatus: http.StatusConflict, wantInertiaLocation: "/dashboard?tab=1"},
		{name: "role unauthenticated json", fixture: unauthenticatedWith(requireAdmin), target: "/admin", kind: kindJSON, wantStatus: http.StatusUnauthorized, wantType: problem.ProblemTypeContent},
		{name: "forbidden json", fixture: signedInAs([]string{"editor"}, requireAdmin), target: "/admin", kind: kindJSON, wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Forbidden"},
		{name: "forbidden browser", fixture: signedInAs(nil, requireAdmin), target: "/admin", kind: kindBrowser, wantStatus: http.StatusForbidden, wantType: "text/html"},
		{name: "forbidden inertia", fixture: signedInAs(nil, requireAdmin), target: "/admin", kind: kindInertia, wantStatus: http.StatusConflict, wantInertiaLocation: "/admin"},
		{name: "ability denied json", fixture: signedInAs(nil, requireAbility("view-reports", false)), target: "/reports", kind: kindJSON, wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Forbidden"},
		{name: "guest json", fixture: signedInAs(nil, guestOnly), target: "/login", kind: kindJSON, wantStatus: http.StatusForbidden, wantType: problem.ProblemTypeContent, wantDetail: "Already authenticated."},
		{name: "guest browser", fixture: signedInAs(nil, guestOnly), target: "/login", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/"},
		{name: "guest browser custom redirect", fixture: signedInAs(nil, guestOnlyToDashboard), target: "/login", kind: kindBrowser, wantStatus: http.StatusSeeOther, wantLocation: "/dashboard"},
		{name: "guest inertia", fixture: signedInAs(nil, guestOnlyToDashboard), target: "/login", kind: kindInertia, wantStatus: http.StatusSeeOther, wantLocation: "/dashboard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, mw := tt.fixture()
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			w, rep := servePipeline(t, m, mw, newDenialRequest(method, tt.target, tt.kind))

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
				if body["status"] != float64(tt.wantStatus) {
					t.Errorf("body status = %v, want %d", body["status"], tt.wantStatus)
				}
			}
			if n := rep.count(); n != 0 {
				t.Errorf("reports = %d, want 0 (denials are client outcomes)", n)
			}
		})
	}
}

// TestAuthMiddleware_StashesIntended asserts the intended URL is stashed
// in the session only for GET requests that do not want JSON, and that
// the browser is bounced to a clean login target.
func TestAuthMiddleware_StashesIntended(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		kind       string
		wantStash  string
		wantStatus int
	}{
		{name: "browser get", method: http.MethodGet, kind: kindBrowser, wantStash: "/settings?tab=profile&page=2", wantStatus: http.StatusSeeOther},
		{name: "inertia get", method: http.MethodGet, kind: kindInertia, wantStash: "/settings?tab=profile&page=2", wantStatus: http.StatusConflict},
		{name: "json get", method: http.MethodGet, kind: kindJSON, wantStatus: http.StatusUnauthorized},
		{name: "browser post", method: http.MethodPost, kind: kindBrowser, wantStatus: http.StatusSeeOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := NewSession("sid")
			m := NewManager()
			m.RegisterScheme("web", &sessionAwareScheme{mockSchemeForMiddleware{authenticated: false}, sess})

			w, _ := servePipeline(t, m, AuthMiddleware(m), newDenialRequest(tt.method, "/settings?tab=profile&page=2", tt.kind))

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusSeeOther {
				if loc := w.Header().Get("Location"); loc != "/login" {
					t.Errorf("Location = %q, want /login (no ?redirect= leak)", loc)
				}
			}
			got, _ := sess.Get(router.IntendedSessionKey).(string)
			if got != tt.wantStash {
				t.Errorf("stashed intended = %q, want %q", got, tt.wantStash)
			}
		})
	}
}

// TestGuestMiddleware_AuthenticatedReturns asserts what the guest guard
// returns for an authenticated user: a 403 error for JSON with nothing
// written, and a written redirect reported as contract.ErrResponseWritten
// otherwise.
func TestGuestMiddleware_AuthenticatedReturns(t *testing.T) {
	tests := []struct {
		name         string
		kind         string
		wantWritten  bool
		wantLocation string
	}{
		{name: "json", kind: kindJSON},
		{name: "browser", kind: kindBrowser, wantWritten: true, wantLocation: "/dashboard"},
		{name: "inertia", kind: kindInertia, wantWritten: true, wantLocation: "/dashboard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newManagerWithScheme(true)
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			c := router.NewContext(w, newDenialRequest(http.MethodGet, "/login", tt.kind))
			err := GuestMiddlewareWithRedirect(m, "/dashboard")(func(*router.Context) error {
				t.Error("next handler should not be called")
				return nil
			})(c)

			if w.wrote != tt.wantWritten {
				t.Errorf("wrote = %v, want %v", w.wrote, tt.wantWritten)
			}
			if !tt.wantWritten {
				var he *contract.HTTPError
				if !errors.As(err, &he) || he.StatusCode() != http.StatusForbidden || he.Message != "Already authenticated." {
					t.Errorf("error = %v, want a 403 HTTPError \"Already authenticated.\"", err)
				}
				return
			}
			if !errors.Is(err, contract.ErrResponseWritten) || contract.HandledCause(err) != nil {
				t.Errorf("error = %v, want the bare ErrResponseWritten", err)
			}
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != tt.wantLocation {
				t.Errorf("response = %d %q, want 303 %q", w.Code, w.Header().Get("Location"), tt.wantLocation)
			}
		})
	}
}
