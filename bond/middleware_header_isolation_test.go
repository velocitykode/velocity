package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// responseBuffer clones the real writer's header map, so the headers a
// handler sets reach the connection only when the middleware commits
// them: with the buffered response, or on an errored empty response,
// where they ride on the error path's answer (a session Set-Cookie saved
// before the error must reach the browser) less the caching headers. An
// empty 200 answered with a redirect back drops them.

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

// An errored empty response keeps the handler's cookie, its Vary values
// and other headers but drops its caching headers, so the error answer is
// never cached.
func TestMiddlewareFunc_ErrorResponseDropsCachingHeaders(t *testing.T) {
	_, rt := newBondRouter(t)
	rt.Get("/boom", func(c *router.Context) error {
		h := c.Response.Header()
		h.Set("Cache-Control", "public, max-age=3600")
		h.Set("ETag", `"v1"`)
		h.Set("Last-Modified", "Mon, 01 Jan 2024 00:00:00 GMT")
		h.Set("Expires", "Mon, 01 Jan 2030 00:00:00 GMT")
		h.Add("Vary", "Accept-Language")
		h.Set("X-Handler-Header", "kept")
		http.SetCookie(c.Response, &http.Cookie{Name: "sid", Value: "abc"})
		return errors.New("boom")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.Header.Set("X-Inertia", "true")
	rt.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if got := w.Header().Get("Set-Cookie"); got != "sid=abc" {
		t.Errorf("Set-Cookie = %q, want %q", got, "sid=abc")
	}
	if got := w.Header().Get("X-Handler-Header"); got != "kept" {
		t.Errorf("X-Handler-Header = %q, want %q", got, "kept")
	}
	for _, key := range []string{"Cache-Control", "ETag", "Last-Modified", "Expires"} {
		if got := w.Header().Get(key); got != "" {
			t.Errorf("%s = %q on the error response, want none", key, got)
		}
	}
	vary := strings.Join(w.Header().Values("Vary"), ", ")
	for _, want := range []string{"X-Inertia", "Accept-Language"} {
		if !strings.Contains(vary, want) {
			t.Errorf("Vary = %q on the error response, want it to keep %s", vary, want)
		}
	}
}
