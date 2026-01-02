package bond

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/pkg/router"
)

func TestMiddleware_SetsVaryHeader(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.ServeHTTP(w, r)

	vary := w.Header().Get("Vary")
	if vary != "X-Inertia" {
		t.Errorf("expected Vary 'X-Inertia', got %s", vary)
	}
}

func TestMiddleware_PassesThroughNonInertiaRequest(t *testing.T) {
	b := setupBond(t)

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestMiddleware_AllowsMatchingVersion(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "abc123")

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called for matching version")
	}
	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestMiddleware_Returns409OnVersionMismatch(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "old-version")

	handler.ServeHTTP(w, r)

	if called {
		t.Error("expected handler NOT to be called on version mismatch")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestMiddleware_SetsXInertiaLocationOnMismatch(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/dashboard?page=2", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "old-version")

	handler.ServeHTTP(w, r)

	location := w.Header().Get("X-Inertia-Location")
	if location != "/dashboard?page=2" {
		t.Errorf("expected X-Inertia-Location '/dashboard?page=2', got %s", location)
	}
}

func TestMiddleware_AllowsNoVersionHeader(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	// No X-Inertia-Version header

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called when no version header")
	}
}

func TestMiddleware_InertiaRequestWithEmptyVersion(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "abc123",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "")

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called for empty version")
	}
}

func TestMiddleware_PreservesVaryHeaderForNonInertia(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	// Non-Inertia request

	handler.ServeHTTP(w, r)

	vary := w.Header().Get("Vary")
	if vary != "X-Inertia" {
		t.Errorf("expected Vary header for non-Inertia request, got %s", vary)
	}
}

func TestMiddleware_ChainMultiple(t *testing.T) {
	b := setupBond(t)

	order := []string{}

	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "before1")
			next.ServeHTTP(w, r)
			order = append(order, "after1")
		})
	}

	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	})

	handler := middleware1(b.Middleware(final))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	handler.ServeHTTP(w, r)

	expected := []string{"before1", "handler", "after1"}
	if len(order) != len(expected) {
		t.Errorf("expected %v, got %v", expected, order)
	}
}

func TestMiddlewareFunc_ReturnsRouterMiddleware(t *testing.T) {
	b := setupBond(t)

	mw := b.MiddlewareFunc()

	if mw == nil {
		t.Error("expected middleware func to be returned")
	}
}

func TestMiddleware_POSTRequest(t *testing.T) {
	b := setupBond(t)

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called for POST request")
	}
}

func TestMiddleware_VersionMismatchOnPOST(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "v1",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "v0")

	handler.ServeHTTP(w, r)

	if called {
		t.Error("expected handler NOT to be called on POST with version mismatch")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}

func TestMiddlewareFunc_IntegrationWithRouter(t *testing.T) {
	b := setupBond(t)

	mw := b.MiddlewareFunc()

	// Create a mock handler
	called := false
	handler := mw(func(c *router.Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Create a router context
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := router.NewContext(w, r)

	// Execute
	handler(ctx)

	if !called {
		t.Error("expected handler to be called")
	}

	// Verify Vary header was set
	vary := w.Header().Get("Vary")
	if vary != "X-Inertia" {
		t.Errorf("expected Vary 'X-Inertia', got %s", vary)
	}
}

func TestMiddlewareFunc_VersionMismatch(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "v1",
	})

	mw := b.MiddlewareFunc()

	called := false
	handler := mw(func(c *router.Context) error {
		called = true
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "v0")
	ctx := router.NewContext(w, r)

	handler(ctx)

	if called {
		t.Error("expected handler NOT to be called on version mismatch")
	}
	if w.Code != http.StatusConflict {
		t.Errorf("expected status 409, got %d", w.Code)
	}
}
