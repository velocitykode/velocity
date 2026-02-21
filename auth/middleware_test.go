package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// mockGuardForMiddleware implements Guard for middleware tests.
type mockGuardForMiddleware struct {
	authenticated bool
	user          Authenticatable
}

func (g *mockGuardForMiddleware) Check(*http.Request) bool { return g.authenticated }
func (g *mockGuardForMiddleware) User(*http.Request) Authenticatable {
	if g.authenticated {
		return g.user
	}
	return nil
}
func (g *mockGuardForMiddleware) ID(*http.Request) interface{} { return nil }
func (g *mockGuardForMiddleware) SetProvider(UserProvider)      {}
func (g *mockGuardForMiddleware) Logout(http.ResponseWriter, *http.Request) error {
	return nil
}
func (g *mockGuardForMiddleware) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *mockGuardForMiddleware) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *mockGuardForMiddleware) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}

func newManagerWithGuard(authenticated bool) *Manager {
	m := NewManager()
	m.RegisterGuard("web", &mockGuardForMiddleware{authenticated: authenticated})
	return m
}

func newManagerWithUser(user *mockUser) *Manager {
	m := NewManager()
	m.RegisterGuard("web", &mockGuardForMiddleware{authenticated: true, user: user})
	return m
}

func TestAuthMiddleware_AllowsAuthenticatedUsers(t *testing.T) {
	m := newManagerWithGuard(true)
	mw := AuthMiddleware(m)

	var called bool
	handler := mw(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	c := router.NewContext(w, r)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called for authenticated user")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuthMiddleware_RedirectsUnauthenticated(t *testing.T) {
	m := newManagerWithGuard(false)
	mw := AuthMiddleware(m)

	handler := mw(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.Header.Set("Accept", "text/html")
	c := router.NewContext(w, r)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header for redirect")
	}
	if loc != "/login?redirect=%2Fdashboard" {
		t.Errorf("Location = %q, want /login?redirect=%%2Fdashboard", loc)
	}
}

func TestAuthMiddleware_ReturnsJSONForAPIRequests(t *testing.T) {
	m := newManagerWithGuard(false)
	mw := AuthMiddleware(m)

	handler := mw(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/user", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON response: %v", err)
	}
	if body["error"] != "Unauthenticated." {
		t.Errorf("error = %q, want %q", body["error"], "Unauthenticated.")
	}
}

func TestAuthMiddleware_ReturnsJSONForXHR(t *testing.T) {
	m := newManagerWithGuard(false)
	mw := AuthMiddleware(m)

	handler := mw(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/user", nil)
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	c := router.NewContext(w, r)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthMiddleware_PreservesQueryParams(t *testing.T) {
	m := newManagerWithGuard(false)
	mw := AuthMiddleware(m)

	handler := mw(func(c *router.Context) error {
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/settings?tab=profile&page=2", nil)
	r.Header.Set("Accept", "text/html")
	c := router.NewContext(w, r)

	err := handler(c)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	loc := w.Header().Get("Location")
	expected := "/login?redirect=%2Fsettings%3Ftab%3Dprofile%26page%3D2"
	if loc != expected {
		t.Errorf("Location = %q, want %q", loc, expected)
	}
}

// --- RequireRole tests ---

func TestRequireRole_AllowsUserWithRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"admin"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	var called bool
	handler := RequireRole(m, "admin")(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestRequireRole_DeniesUserWithoutRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"editor"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	handler := RequireRole(m, "admin")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body["error"] != "Forbidden." {
		t.Errorf("error = %q, want %q", body["error"], "Forbidden.")
	}
}

func TestRequireRole_ReturnsUnauthorizedWhenNotAuthenticated(t *testing.T) {
	m := newManagerWithGuard(false)

	handler := RequireRole(m, "admin")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestRequireRole_HTMLForbidden(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(Authenticatable, string) bool { return false })

	handler := RequireRole(m, "admin")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Header.Set("Accept", "text/html")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// --- RequireAnyRole tests ---

func TestRequireAnyRole_AllowsUserWithOneMatchingRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"editor"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	var called bool
	handler := RequireAnyRole(m, "admin", "editor")(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestRequireAnyRole_DeniesUserWithNoMatchingRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"viewer"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	handler := RequireAnyRole(m, "admin", "editor")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestRequireAnyRole_ReturnsUnauthorizedWhenNotAuthenticated(t *testing.T) {
	m := newManagerWithGuard(false)

	handler := RequireAnyRole(m, "admin", "editor")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/dashboard", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// --- RequireAllRoles tests ---

func TestRequireAllRoles_AllowsUserWithAllRoles(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"admin", "editor"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	var called bool
	handler := RequireAllRoles(m, "admin", "editor")(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestRequireAllRoles_DeniesUserMissingOneRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"admin"}}
	m := newManagerWithUser(user)
	m.Gate().SetRoleChecker(func(u Authenticatable, role string) bool {
		mu := u.(*mockUser)
		for _, r := range mu.roles {
			if r == role {
				return true
			}
		}
		return false
	})

	handler := RequireAllRoles(m, "admin", "editor")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestRequireAllRoles_ReturnsUnauthorizedWhenNotAuthenticated(t *testing.T) {
	m := newManagerWithGuard(false)

	handler := RequireAllRoles(m, "admin", "editor")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// --- AuthorizeMiddleware tests ---

func TestAuthorizeMiddleware_AllowsWhenAbilityGranted(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Gate().Define("view-reports", func(u Authenticatable, args ...interface{}) bool {
		return true
	})

	var called bool
	handler := AuthorizeMiddleware(m, "view-reports")(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/reports", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestAuthorizeMiddleware_DeniesWhenAbilityDenied(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Gate().Define("view-reports", func(u Authenticatable, args ...interface{}) bool {
		return false
	})

	handler := AuthorizeMiddleware(m, "view-reports")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/reports", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body["error"] != "Forbidden." {
		t.Errorf("error = %q, want %q", body["error"], "Forbidden.")
	}
}

func TestAuthorizeMiddleware_WithResourceFunc(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Gate().Define("edit-post", func(u Authenticatable, args ...interface{}) bool {
		if len(args) > 0 {
			postOwner, ok := args[0].(int)
			if ok {
				return u.GetAuthIdentifier() == postOwner
			}
		}
		return false
	})

	var called bool
	handler := AuthorizeMiddleware(m, "edit-post", func(c *router.Context) interface{} {
		return 1 // post owner ID matches user ID
	})(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/posts/1", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestAuthorizeMiddleware_WithResourceFunc_Denied(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Gate().Define("edit-post", func(u Authenticatable, args ...interface{}) bool {
		if len(args) > 0 {
			postOwner, ok := args[0].(int)
			if ok {
				return u.GetAuthIdentifier() == postOwner
			}
		}
		return false
	})

	handler := AuthorizeMiddleware(m, "edit-post", func(c *router.Context) interface{} {
		return 999 // different owner
	})(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/posts/1", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestAuthorizeMiddleware_ReturnsUnauthorizedWhenNotAuthenticated(t *testing.T) {
	m := newManagerWithGuard(false)

	handler := AuthorizeMiddleware(m, "view-reports")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/reports", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuthorizeMiddleware_UndefinedAbilityDenied(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	// No gate defined for "fly" — should deny by default

	handler := AuthorizeMiddleware(m, "fly")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/fly", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// --- GuestMiddleware tests ---

func TestGuestMiddleware_AllowsUnauthenticatedUsers(t *testing.T) {
	m := newManagerWithGuard(false)

	var called bool
	handler := GuestMiddleware(m)(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "login page")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/login", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called for unauthenticated user")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestGuestMiddleware_RedirectsAuthenticatedHTML(t *testing.T) {
	m := newManagerWithGuard(true)

	handler := GuestMiddleware(m)(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/login", nil)
	r.Header.Set("Accept", "text/html")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc != "/" {
		t.Errorf("Location = %q, want %q", loc, "/")
	}
}

func TestGuestMiddleware_ReturnsForbiddenForJSON(t *testing.T) {
	m := newManagerWithGuard(true)

	handler := GuestMiddleware(m)(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/login", nil)
	r.Header.Set("Accept", "application/json")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}

	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if body["error"] != "Already authenticated." {
		t.Errorf("error = %q, want %q", body["error"], "Already authenticated.")
	}
}

func TestGuestMiddlewareWithRedirect_UsesCustomURL(t *testing.T) {
	m := newManagerWithGuard(true)

	handler := GuestMiddlewareWithRedirect(m, "/dashboard")(func(c *router.Context) error {
		t.Error("next handler should not be called")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/login", nil)
	r.Header.Set("Accept", "text/html")
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if w.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	loc := w.Header().Get("Location")
	if loc != "/dashboard" {
		t.Errorf("Location = %q, want %q", loc, "/dashboard")
	}
}

func TestGuestMiddlewareWithRedirect_AllowsUnauthenticated(t *testing.T) {
	m := newManagerWithGuard(false)

	var called bool
	handler := GuestMiddlewareWithRedirect(m, "/dashboard")(func(c *router.Context) error {
		called = true
		return c.String(http.StatusOK, "register page")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/register", nil)
	c := router.NewContext(w, r)

	if err := handler(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if !called {
		t.Error("next handler was not called for unauthenticated user")
	}
}
