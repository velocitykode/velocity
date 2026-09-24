package auth

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/router"
)

func TestAuditSalt_DeterministicAfterSet(t *testing.T) {
	SetAuditSalt([]byte("abc"))

	first := hashRemoteAddr("1.2.3.4")
	second := hashRemoteAddr("1.2.3.4")

	if first != second {
		t.Fatalf("hashRemoteAddr returned unstable digest: first=%q second=%q", first, second)
	}
	if first != "257d18a9bc09bc44" {
		t.Fatalf("hashRemoteAddr digest = %q, want %q", first, "257d18a9bc09bc44")
	}
}

func TestAuditSalt_ConcurrentSetAndGet(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "1.2.3.4:5555"

	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				SetAuditSalt([]byte("fixed"))
			}
		}()
	}

	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_ = hashClientIP(nil, req)
			}
		}()
	}

	close(start)
	wg.Wait()
}

// mockSchemeForMiddleware implements Scheme for middleware tests.
type mockSchemeForMiddleware struct {
	authenticated bool
	user          Authenticatable
}

func (g *mockSchemeForMiddleware) Check(*http.Request) bool { return g.authenticated }
func (g *mockSchemeForMiddleware) User(*http.Request) Authenticatable {
	if g.authenticated {
		return g.user
	}
	return nil
}
func (g *mockSchemeForMiddleware) ID(*http.Request) interface{} { return nil }
func (g *mockSchemeForMiddleware) SetUserStore(UserStore)       {}
func (g *mockSchemeForMiddleware) Logout(http.ResponseWriter, *http.Request) error {
	return nil
}
func (g *mockSchemeForMiddleware) Login(http.ResponseWriter, *http.Request, Authenticatable, ...bool) error {
	return nil
}
func (g *mockSchemeForMiddleware) LoginByID(http.ResponseWriter, *http.Request, interface{}, ...bool) error {
	return nil
}
func (g *mockSchemeForMiddleware) Attempt(http.ResponseWriter, *http.Request, map[string]interface{}, ...bool) (bool, error) {
	return false, nil
}

func newManagerWithScheme(authenticated bool) *Manager {
	m := NewManager()
	m.RegisterScheme("web", &mockSchemeForMiddleware{authenticated: authenticated})
	return m
}

func newManagerWithUser(user *mockUser) *Manager {
	m := NewManager()
	m.RegisterScheme("web", &mockSchemeForMiddleware{authenticated: true, user: user})
	return m
}

func TestAuthMiddleware_AllowsAuthenticatedUsers(t *testing.T) {
	m := newManagerWithScheme(true)
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

// sessionAwareScheme is a mock scheme that also satisfies SessionAware so
// manager.Session(r) returns a real session the stash path can write to.
type sessionAwareScheme struct {
	mockSchemeForMiddleware
	sess Session
}

func (g *sessionAwareScheme) Session(*http.Request) Session { return g.sess }

// --- RequireRole tests ---

func TestRequireRole_AllowsUserWithRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"admin"}}
	m := newManagerWithUser(user)
	m.Access().SetRoleChecker(func(u Authenticatable, role string) bool {
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

// --- RequireAnyRole tests ---

func TestRequireAnyRole_AllowsUserWithOneMatchingRole(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"editor"}}
	m := newManagerWithUser(user)
	m.Access().SetRoleChecker(func(u Authenticatable, role string) bool {
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

// --- RequireAllRoles tests ---

func TestRequireAllRoles_AllowsUserWithAllRoles(t *testing.T) {
	user := &mockUser{id: 1, roles: []string{"admin", "editor"}}
	m := newManagerWithUser(user)
	m.Access().SetRoleChecker(func(u Authenticatable, role string) bool {
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

// --- AuthorizeMiddleware tests ---

func TestAuthorizeMiddleware_AllowsWhenAbilityGranted(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Access().Define("view-reports", func(u Authenticatable, args ...interface{}) bool {
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

func TestAuthorizeMiddleware_WithResourceFunc(t *testing.T) {
	user := &mockUser{id: 1}
	m := newManagerWithUser(user)
	m.Access().Define("edit-post", func(u Authenticatable, args ...interface{}) bool {
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

// --- GuestMiddleware tests ---

func TestGuestMiddleware_AllowsUnauthenticatedUsers(t *testing.T) {
	m := newManagerWithScheme(false)

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

func TestGuestMiddlewareWithRedirect_AllowsUnauthenticated(t *testing.T) {
	m := newManagerWithScheme(false)

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
