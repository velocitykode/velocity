package bond

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

// responseBuffer clones the real writer's header map, so the headers a
// handler sets reach the connection only when the middleware commits
// them: with the buffered response, or on an errored empty response,
// where they ride on the error path's answer (a session Set-Cookie saved
// before the error must reach the browser) less the handler's caching
// headers. An empty 200 answered with a redirect back drops them.

func TestMiddlewareFunc_HandlerHeadersKeptOnErrorResponse(t *testing.T) {
	b := setupBond(t)
	rt := router.NewV2()
	rt.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response.Header().Set("X-Outer", "set before bond")
			return next(c)
		}
	})
	rt.Use(b.MiddlewareFunc())
	rt.Get("/boom", func(c *router.Context) error {
		c.Response.Header().Set("X-Handler-Header", "kept")
		http.SetCookie(c.Response, &http.Cookie{Name: "sid", Value: "abc"})
		c.Response.Header().Del("X-Outer")
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
	if got := w.Header().Get("X-Outer"); got != "" {
		t.Errorf("X-Outer = %q, want the handler's delete kept", got)
	}
	if got := w.Header().Values("Vary"); len(got) != 1 || got[0] != "X-Inertia" {
		t.Errorf("Vary = %q, want the error answer's [X-Inertia]", got)
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
// never cached under the handler's directives. Caching headers set by
// middleware that ran before bond stay exactly as that middleware set
// them, whatever the handler did to them.
func TestMiddlewareFunc_ErrorResponseDropsCachingHeaders(t *testing.T) {
	handlerCaching := map[string]string{
		"Cache-Control": "public, max-age=3600",
		"ETag":          `"v1"`,
		"Last-Modified": "Mon, 01 Jan 2024 00:00:00 GMT",
		"Expires":       "Mon, 01 Jan 2030 00:00:00 GMT",
	}
	tests := []struct {
		name    string
		outer   map[string][]string // caching headers set before bond
		handler func(h http.Header) // caching headers the handler touches
		want    map[string][]string // caching headers on the error response
	}{
		{
			name: "HandlerSetDropped",
			handler: func(h http.Header) {
				for k, v := range handlerCaching {
					h.Set(k, v)
				}
			},
			want: map[string][]string{},
		},
		{
			name:    "OuterSetKept",
			outer:   map[string][]string{"Cache-Control": {"private, no-store"}, "Expires": {"0"}},
			handler: func(http.Header) {},
			want:    map[string][]string{"Cache-Control": {"private, no-store"}, "Expires": {"0"}},
		},
		{
			name:    "OuterMultiValueKept",
			outer:   map[string][]string{"Cache-Control": {"private", "no-store"}},
			handler: func(http.Header) {},
			want:    map[string][]string{"Cache-Control": {"private", "no-store"}},
		},
		{
			name:  "OuterOverwrittenByHandlerRestored",
			outer: map[string][]string{"Cache-Control": {"private, no-store"}},
			handler: func(h http.Header) {
				for k, v := range handlerCaching {
					h.Set(k, v)
				}
			},
			want: map[string][]string{"Cache-Control": {"private, no-store"}},
		},
		{
			name:    "OuterDeletedByHandlerRestored",
			outer:   map[string][]string{"Cache-Control": {"private, no-store"}},
			handler: func(h http.Header) { h.Del("Cache-Control") },
			want:    map[string][]string{"Cache-Control": {"private, no-store"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := setupBond(t)
			rt := router.NewV2()
			rt.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					for k, vs := range tt.outer {
						for _, v := range vs {
							c.Response.Header().Add(k, v)
						}
					}
					return next(c)
				}
			})
			rt.Use(b.MiddlewareFunc())
			rt.Get("/boom", func(c *router.Context) error {
				h := c.Response.Header()
				tt.handler(h)
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
				if got, want := w.Header().Values(key), tt.want[key]; !slices.Equal(got, want) {
					t.Errorf("%s = %q on the error response, want %q", key, got, want)
				}
			}
			vary := strings.Join(w.Header().Values("Vary"), ", ")
			for _, want := range []string{"X-Inertia", "Accept-Language"} {
				if !strings.Contains(vary, want) {
					t.Errorf("Vary = %q on the error response, want it to keep %s", vary, want)
				}
			}
		})
	}
}
