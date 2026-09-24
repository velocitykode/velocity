package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// responseBuffer clones the real writer's header map, so the headers a
// handler sets reach the connection only when the middleware commits
// them: with the buffered response, or on an errored empty response,
// where they ride on the error path's answer (a session Set-Cookie saved
// before the error must reach the browser). An empty 200 answered with a
// redirect back drops them.

func TestMiddlewareFunc_HandlerHeadersKeptOnErrorResponse(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/boom", func(c *router.Context) error {
		c.Response.Header().Set("X-Handler-Header", "kept")
		http.SetCookie(c.Response, &http.Cookie{Name: "sid", Value: "abc"})
		c.Response.Header().Del("Vary")
		return errors.New("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.Header.Set("X-Inertia", "true")

	rt.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if got := w.Header().Get("X-Handler-Header"); got != "kept" {
		t.Errorf("X-Handler-Header = %q, want %q", got, "kept")
	}
	if got := w.Header().Get("Set-Cookie"); got != "sid=abc" {
		t.Errorf("Set-Cookie = %q, want %q", got, "sid=abc")
	}
	if got := w.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want the handler's delete kept", got)
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
