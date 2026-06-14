package bond

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// precommitWriter models the router responseWriter's BeforeFirstWrite
// contract: a hook registered by an outer middleware (the session guard
// writing its Set-Cookie is the real case) fires when the response is first
// committed, and writes to whatever the Context's response currently is. The
// bond buffered (Inertia) path must have c.Response pointing at this real
// writer when the hook fires; if it still points at the drained buffer the
// late Set-Cookie is lost. See MiddlewareFunc.
type precommitWriter struct {
	http.ResponseWriter
	hook  func()
	fired bool
}

func (p *precommitWriter) WriteHeader(code int) {
	if !p.fired {
		p.fired = true
		if p.hook != nil {
			p.hook()
		}
	}
	p.ResponseWriter.WriteHeader(code)
}

// TestMiddlewareFunc_PrecommitHookReachesRealWriter pins the fix for the
// bond-buffering bug where a session-guard precommit Set-Cookie was dropped on
// Inertia responses: the hook fired during flush's orig.WriteHeader while
// c.Response still pointed at the buffer, so the cookie landed in the buffer's
// header map after flush had already copied headers out. Symptom downstream:
// the CSRF token bound to the fresh session id round-tripped no session cookie,
// 419ing the next POST.
func TestMiddlewareFunc_PrecommitHookReachesRealWriter(t *testing.T) {
	b := setupBond(t)
	rec := httptest.NewRecorder()
	orig := &precommitWriter{ResponseWriter: rec}
	r := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.Header.Set("X-Inertia", "true")
	c := router.NewContext(orig, r)

	// Outer-middleware precommit: writes a Set-Cookie to the LIVE c.Response
	// at commit time, exactly as the session guard's deferred save does.
	orig.hook = func() {
		http.SetCookie(c.Response, &http.Cookie{Name: "velship_session", Value: "s1", Path: "/"})
	}

	mw := b.MiddlewareFunc()
	h := mw(func(c *router.Context) error {
		// Inertia JSON body; the handler never touches the session cookie.
		_, err := c.Response.Write([]byte(`{"component":"Auth/Login"}`))
		return err
	})
	if err := h(c); err != nil {
		t.Fatalf("handler returned error: %v", err)
	}

	if got := rec.Header().Get("Set-Cookie"); !strings.Contains(got, "velship_session=s1") {
		t.Fatalf("precommit Set-Cookie dropped through buffered flush; Set-Cookie=%q", got)
	}
}

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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"component":"Home"}`))
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
		w.Write([]byte(`{"component":"Home"}`))
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
		w.Write([]byte(`{"component":"Home"}`))
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
		w.Write([]byte(`{"component":"Home"}`))
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if !called {
		t.Error("expected handler to be called for POST request")
	}
}

func TestMiddleware_VersionMismatchOnPOST_Skipped(t *testing.T) {
	b, _ := New(Config{
		RootTemplate: validTemplate,
		Version:      "v1",
	})

	called := false
	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		// Simulate a redirect (typical POST handler behavior)
		http.Redirect(w, r, "/submit", http.StatusSeeOther)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("X-Inertia-Version", "v0")

	handler.ServeHTTP(w, r)

	// POST with version mismatch should still process (not 409)
	// to avoid discarding form data
	if !called {
		t.Error("expected handler to be called on POST even with version mismatch")
	}
	if w.Code == http.StatusConflict {
		t.Error("POST should not return 409 on version mismatch")
	}
}

func TestMiddlewareFunc_IntegrationWithRouter(t *testing.T) {
	b := setupBond(t)

	mw := b.MiddlewareFunc()

	called := false
	handler := mw(func(c *router.Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		c.Response.Write([]byte(`{"component":"Home"}`))
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := router.NewContext(w, r)

	handler(ctx)

	if !called {
		t.Error("expected handler to be called")
	}

	vary := w.Header().Get("Vary")
	if vary != "X-Inertia" {
		t.Errorf("expected Vary 'X-Inertia', got %s", vary)
	}
}

func TestMiddleware_Rewrites302To303ForPUT(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/users/1", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
}

func TestMiddleware_Rewrites302To303ForPATCH(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPatch, "/users/1", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
}

func TestMiddleware_Rewrites302To303ForDELETE(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodDelete, "/users/1", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303, got %d", w.Code)
	}
}

func TestMiddleware_Preserves302ForPOST(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/users", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 preserved for POST, got %d", w.Code)
	}
}

func TestMiddleware_Preserves302ForGET(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/old-page", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 preserved for GET, got %d", w.Code)
	}
}

func TestMiddleware_EmptyResponseRedirectsBack(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handler returns nothing — forgot to call Render or Redirect
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("Referer", "/form")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect back, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if location != "/form" {
		t.Errorf("expected redirect to /form, got %s", location)
	}
}

func TestMiddleware_EmptyResponseFallsBackToRoot(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handler returns nothing, no Referer header
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect back, got %d", w.Code)
	}
	location := w.Header().Get("Location")
	if location != "/" {
		t.Errorf("expected redirect to /, got %s", location)
	}
}

func TestMiddleware_NonEmptyResponseNotRedirected(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"component":"Home"}`))
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if w.Body.String() != `{"component":"Home"}` {
		t.Errorf("expected body to be preserved, got %s", w.Body.String())
	}
}

func TestMiddleware_302NotRewrittenForNonInertia(t *testing.T) {
	b := setupBond(t)

	handler := b.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPut, "/users/1", nil)
	// No X-Inertia header — non-Inertia request

	handler.ServeHTTP(w, r)

	if w.Code != http.StatusFound {
		t.Errorf("expected 302 unchanged for non-Inertia, got %d", w.Code)
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
