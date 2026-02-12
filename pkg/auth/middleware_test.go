package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/pkg/router"
)

// mockGuardForMiddleware implements Guard for middleware tests.
type mockGuardForMiddleware struct {
	authenticated bool
}

func (g *mockGuardForMiddleware) Check(*http.Request) bool           { return g.authenticated }
func (g *mockGuardForMiddleware) User(*http.Request) Authenticatable { return nil }
func (g *mockGuardForMiddleware) ID(*http.Request) interface{}       { return nil }
func (g *mockGuardForMiddleware) SetProvider(UserProvider)           {}
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
