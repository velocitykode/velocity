package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// Regression: responseBuffer shared the real writer's header map by
// pointer, so headers the handler set leaked onto the real connection
// even when the buffered body was discarded (handler error -> router
// error path, empty 200 -> redirect back). The buffer now clones the
// header map and flush commits it only when the buffered response is
// actually written.

func TestMiddlewareFunc_HandlerHeadersDoNotLeakIntoErrorResponse(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/boom", func(c *router.Context) error {
		c.Response.Header().Set("X-Handler-Secret", "leaked")
		c.Response.Header().Set("Cache-Control", "public, max-age=3600")
		return errors.New("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if got := w.Header().Get("X-Handler-Secret"); got != "" {
		t.Errorf("handler header leaked into error response: X-Handler-Secret=%q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "" {
		t.Errorf("handler header leaked into error response: Cache-Control=%q", got)
	}
}

func TestMiddlewareFunc_HandlerHeadersDoNotLeakIntoRedirectBack(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/empty", func(c *router.Context) error {
		// Sets a header but writes nothing: bond redirects back.
		c.Response.Header().Set("X-Handler-Secret", "leaked")
		return nil
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/empty", nil)
	r.Header.Set("X-Inertia", "true")
	r.Header.Set("Referer", "/somewhere")

	rt.ServeHTTP(w, r)

	if w.Code < 300 || w.Code >= 400 {
		t.Fatalf("expected redirect back on empty 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Handler-Secret"); got != "" {
		t.Errorf("handler header leaked into redirect-back response: X-Handler-Secret=%q", got)
	}
}

func TestMiddlewareFunc_HandlerHeadersCommitOnSuccess(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/ok", func(c *router.Context) error {
		c.Response.Header().Set("X-Handler-Header", "kept")
		c.Response.WriteHeader(http.StatusOK)
		_, err := c.Response.Write([]byte(`{"component":"Home"}`))
		return err
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := w.Header().Get("X-Handler-Header"); got != "kept" {
		t.Errorf("handler header missing from committed response: X-Handler-Header=%q, want %q", got, "kept")
	}
	// Pre-handler headers (bond's own Vary) must survive the flush.
	if got := w.Header().Get("Vary"); got == "" {
		t.Error("pre-handler Vary header lost on flush")
	}
}
