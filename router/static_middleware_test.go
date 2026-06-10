package router

// Tests for OWASP finding V2-01: static file responses must run through
// the global middleware chain (Router.Use), and the chain must run
// exactly once per request regardless of which terminal (static file,
// matched route, 404) handles it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// chainCountMiddleware increments counter and stamps a header so tests
// can assert both "ran" and "ran exactly once".
func chainCountMiddleware(counter *int32, header string) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			atomic.AddInt32(counter, 1)
			c.Response.Header().Set(header, "applied")
			return next(c)
		}
	}
}

func writeStaticFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStaticMiddleware_AppliesToStaticFiles(t *testing.T) {
	modes := []struct {
		name  string
		setup func(r *VelocityRouterV2, dir string)
	}{
		{"Static", func(r *VelocityRouterV2, dir string) { r.Static(dir) }},
		{"StaticFallback", func(r *VelocityRouterV2, dir string) { r.StaticFallback(dir) }},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			dir := t.TempDir()
			writeStaticFile(t, dir, "app.css", "body{}")

			r := NewV2()
			var calls int32
			r.Use(chainCountMiddleware(&calls, "X-Test-MW"))
			mode.setup(r, dir)

			req := httptest.NewRequest("GET", "/app.css", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "body{}") {
				t.Errorf("body = %q, want file content", rec.Body.String())
			}
			if rec.Header().Get("X-Test-MW") != "applied" {
				t.Error("global middleware did not run on static response")
			}
			if n := atomic.LoadInt32(&calls); n != 1 {
				t.Errorf("middleware ran %d times, want exactly 1", n)
			}
		})
	}
}

func TestStaticMiddleware_RunsOnceOnFallthroughToRoute(t *testing.T) {
	dir := t.TempDir() // empty: every path misses static

	r := NewV2()
	var calls int32
	r.Use(chainCountMiddleware(&calls, "X-Test-MW"))
	r.Static(dir)
	r.Get("/api", func(c *Context) error {
		return c.String(http.StatusOK, "api")
	})

	req := httptest.NewRequest("GET", "/api", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "api") {
		t.Errorf("body = %q, want route response", rec.Body.String())
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("middleware ran %d times on static-miss + route-match, want exactly 1", n)
	}
}

func TestStaticMiddleware_404OnBothPathsStillWrapped(t *testing.T) {
	dir := t.TempDir()

	r := NewV2()
	var calls int32
	r.Use(chainCountMiddleware(&calls, "X-Test-MW"))
	r.Static(dir)

	req := httptest.NewRequest("GET", "/nope", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("X-Test-MW") != "applied" {
		t.Error("global middleware did not run on 404 response")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("middleware ran %d times on miss-everything path, want exactly 1", n)
	}
}

func TestStaticMiddleware_CanBlockStaticFile(t *testing.T) {
	dir := t.TempDir()
	writeStaticFile(t, dir, "secret.txt", "secret-content")

	r := NewV2()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Response.WriteHeader(http.StatusForbidden)
			return nil // never calls next: gate closed
		}
	})
	r.Static(dir)

	req := httptest.NewRequest("GET", "/secret.txt", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from blocking middleware", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "secret-content") {
		t.Error("file content leaked past blocking middleware")
	}
}

func TestStaticMiddleware_EventsFireOnceWithStaticRoute(t *testing.T) {
	dir := t.TempDir()
	writeStaticFile(t, dir, "app.js", "console.log(1)")

	r := NewV2()
	r.Use(chainCountMiddleware(new(int32), "X-Test-MW"))
	r.Static(dir)
	r.Get("/api", func(c *Context) error {
		return c.String(http.StatusOK, "api")
	})

	var routed, handled []string
	r.SetEventDispatcher(func(ctx context.Context, event interface{}) error {
		switch e := event.(type) {
		case *RequestRouted:
			routed = append(routed, e.Route)
		case *RequestHandled:
			handled = append(handled, e.Route)
		}
		return nil
	})

	// Static hit: exactly one RequestRouted and one RequestHandled, both "[static]".
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/app.js", nil))
	if len(routed) != 1 || routed[0] != "[static]" {
		t.Errorf("RequestRouted on static hit = %v, want exactly [\"[static]\"]", routed)
	}
	if len(handled) != 1 || handled[0] != "[static]" {
		t.Errorf("RequestHandled on static hit = %v, want exactly [\"[static]\"]", handled)
	}

	// Static miss + route match: no spurious "[static]" RequestRouted.
	routed, handled = nil, nil
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api", nil))
	if len(routed) != 1 || routed[0] != "/api" {
		t.Errorf("RequestRouted on fallthrough = %v, want exactly [\"/api\"]", routed)
	}
	if len(handled) != 1 || handled[0] != "/api" {
		t.Errorf("RequestHandled on fallthrough = %v, want exactly [\"/api\"]", handled)
	}
}

func TestStaticMiddleware_DotDotPathMatchesFileServerCleaning(t *testing.T) {
	// http.FileServer path.Cleans the URL before opening ("/../x" opens
	// "/x"). The probe must predict that identically: a Cleaned path that
	// hits a file is served through the chain; one that misses falls
	// through to the wrapped 404.
	dir := t.TempDir()
	writeStaticFile(t, dir, "x", "x-content")

	r := NewV2()
	var calls int32
	r.Use(chainCountMiddleware(&calls, "X-Test-MW"))
	r.Static(dir)

	req := httptest.NewRequest("GET", "/x", nil)
	req.URL.Path = "/../x" // bypass httptest cleaning
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (FileServer Cleans /../x to /x)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "x-content") {
		t.Errorf("body = %q, want file content", rec.Body.String())
	}
	if rec.Header().Get("X-Test-MW") != "applied" {
		t.Error("global middleware did not run on static response")
	}

	// Cleaned path misses: falls through to middleware-wrapped 404,
	// chain still runs exactly once for that request.
	atomic.StoreInt32(&calls, 0)
	req = httptest.NewRequest("GET", "/y", nil)
	req.URL.Path = "/../y"
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if rec.Header().Get("X-Test-MW") != "applied" {
		t.Error("global middleware did not run on 404 fallthrough")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("middleware ran %d times, want exactly 1", n)
	}
}
