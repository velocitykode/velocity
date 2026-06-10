package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// These tests drive bond's MiddlewareFunc through a real V2 router so
// the full error pipeline (router responseWriter, handleError, custom
// ErrorHandler) is exercised, not just the middleware in isolation.

func newBondRouter(t *testing.T) (*Bond, *router.VelocityRouterV2) {
	t.Helper()
	b := setupBond(t)
	rt := router.NewV2()
	rt.Use(b.MiddlewareFunc())
	return b, rt
}

func TestMiddlewareFunc_HandlerError_DefaultPathReturns500(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/boom", func(c *router.Context) error {
		return errors.New("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 from default error path, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("expected no Location header on handler error, got %q", loc)
	}
}

func TestMiddlewareFunc_HandlerError_CustomErrorHandlerOwnsResponse(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.ErrorHandler = func(c *router.Context, err error) {
		c.Response.WriteHeader(http.StatusTeapot)
		c.Response.Write([]byte("custom-error-page"))
	}
	rt.Get("/boom", func(c *router.Context) error {
		return errors.New("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusTeapot {
		t.Errorf("expected custom error status 418, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "custom-error-page") {
		t.Errorf("expected custom error body to reach the client, got %q", w.Body.String())
	}
}

func TestMiddlewareFunc_HandlerErrorAfterWrite_KeepsBufferedResponse(t *testing.T) {
	// A handler that wrote buffered output before erroring keeps
	// today's flush-then-return-error behavior.
	_, rt := newBondRouter(t)
	rt.Get("/partial", func(c *router.Context) error {
		c.Response.WriteHeader(http.StatusUnprocessableEntity)
		c.Response.Write([]byte(`{"component":"Form"}`))
		return errors.New("late failure")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/partial", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("expected buffered 422 to win, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `{"component":"Form"}`) {
		t.Errorf("expected buffered body to be flushed, got %q", w.Body.String())
	}
}

func TestMiddlewareFunc_EmptyResponseWithoutError_StillRedirectsBack(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Post("/submit", func(c *router.Context) error {
		// Forgot to render or redirect, but no error either.
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/submit", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("Referer", "/form")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Errorf("expected 303 redirect back, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/form" {
		t.Errorf("expected redirect to /form, got %q", loc)
	}
}

func TestMiddlewareFunc_302RewrittenTo303_ThroughRouter(t *testing.T) {
	_, rt := newBondRouter(t)
	for _, register := range []func(string, router.HandlerFunc) router.RouteConfig{rt.Put, rt.Patch, rt.Delete} {
		register("/users/1", func(c *router.Context) error {
			http.Redirect(c.Response, c.Request, "/dashboard", http.StatusFound)
			return nil
		})
	}

	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/users/1", nil)
		r.Header.Set("X-Inertia", "true")

		rt.ServeHTTP(w, r)

		if w.Code != http.StatusSeeOther {
			t.Errorf("%s: expected 302 rewritten to 303, got %d", method, w.Code)
		}
	}
}

func TestMiddlewareFunc_RestoresContextResponseAfterServing(t *testing.T) {
	b := setupBond(t)
	mw := b.MiddlewareFunc()

	var inHandler http.ResponseWriter
	handler := mw(func(c *router.Context) error {
		inHandler = c.Response
		c.Response.WriteHeader(http.StatusOK)
		c.Response.Write([]byte(`{"component":"Home"}`))
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")
	ctx := router.NewContext(w, r)

	if err := handler(ctx); err != nil {
		t.Fatalf("handler: %v", err)
	}

	if _, buffered := inHandler.(*responseBuffer); !buffered {
		t.Errorf("expected handler to see the response buffer, got %T", inHandler)
	}
	if ctx.Response != http.ResponseWriter(w) {
		t.Errorf("expected c.Response restored to the real writer, got %T", ctx.Response)
	}
}

// TestVary_ComposesAcrossMiddlewareStack pins B22: CORS, HTTPS redirect
// and bond each contribute their Vary value instead of clobbering the
// others' cache keys.
func TestVary_ComposesAcrossMiddlewareStack(t *testing.T) {
	b := setupBond(t)
	rt := router.NewV2()
	rt.Use(
		b.MiddlewareFunc(),
		router.CORS(router.CORSConfig{
			AllowedOrigins: []string{"https://app.example.com"},
			AllowedMethods: []string{"GET"},
		}),
		router.HTTPSRedirect(),
	)
	rt.Get("/page", func(c *router.Context) error {
		t.Fatal("handler must not run: HTTPSRedirect should respond first")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "http://example.com/page", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("Origin", "https://app.example.com")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("expected 301 from HTTPSRedirect, got %d", w.Code)
	}
	vary := strings.Join(w.Header().Values("Vary"), ", ")
	for _, want := range []string{"X-Inertia", "Origin", "Host"} {
		if !varyLists(w.Header(), want) {
			t.Errorf("expected Vary to contain %q, got %q", want, vary)
		}
	}
}

func TestAppendVary_DoesNotDuplicate(t *testing.T) {
	h := http.Header{}
	appendVary(h, "X-Inertia")
	appendVary(h, "X-Inertia")
	appendVary(h, "x-inertia")

	if got := h.Values("Vary"); len(got) != 1 {
		t.Errorf("expected a single Vary entry, got %v", got)
	}
}

// varyLists reports whether value appears as a member of any Vary entry.
func varyLists(h http.Header, value string) bool {
	for _, entry := range h.Values("Vary") {
		for _, part := range strings.Split(entry, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}
