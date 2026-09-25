package router

// Tests for OWASP finding V2-01: static file responses must run through
// the global middleware chain (Router.Use), and the chain must run
// exactly once per request regardless of which terminal (static file,
// matched route, 404) handles it.

import (
	"context"
	"fmt"
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

// TestStatic_FileServerFailuresReachTheBoundary asserts, on a standalone
// router, that a request the static file server cannot answer as asked
// comes back to the router's default boundary as an HTTP error instead of
// net/http's own body: the 416 keeps its Content-Range and answers
// problem+json to a JSON client, a server-side open failure is a 500
// logged at error level with a RequestFailed, a path segment too long for
// the file system is a static miss answered 404 by routing with nothing
// logged, and the caching headers middleware set for the file are dropped
// from every error answer.
func TestStatic_FileServerFailuresReachTheBoundary(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		header     [2]string
		wantStatus int
		wantRange  string
		wantErrors int // error log lines and RequestFailed events
	}{
		{name: "unsatisfiable range", path: "/ok.txt", header: [2]string{"Range", "bytes=100-200"}, wantStatus: http.StatusRequestedRangeNotSatisfiable, wantRange: "bytes */3"},
		{name: "failed precondition", path: "/ok.txt", header: [2]string{"If-Match", `"nope"`}, wantStatus: http.StatusPreconditionFailed},
		{name: "open failure", path: "/loop.txt", wantStatus: http.StatusInternalServerError, wantErrors: 1},
		{name: "segment over NAME_MAX", path: "/" + strings.Repeat("a", 300), wantStatus: http.StatusNotFound},
	}
	for _, fallback := range []bool{false, true} {
		for _, tt := range tests {
			t.Run(fmt.Sprintf("fallback=%v/%s", fallback, tt.name), func(t *testing.T) {
				dir := t.TempDir()
				writeStaticFile(t, dir, "ok.txt", "abc")
				if err := os.Symlink("loop.txt", filepath.Join(dir, "loop.txt")); err != nil {
					t.Skipf("symlink: %v", err)
				}
				var errorLines atomic.Int32
				collector := newTestEventCollector()
				r := New()
				r.SetErrorLogger(func(string, ...any) { errorLines.Add(1) })
				r.SetEventDispatcher(collector.dispatch)
				r.Use(func(next HandlerFunc) HandlerFunc {
					return func(c *Context) error {
						c.Response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
						return next(c)
					}
				})
				if fallback {
					r.StaticFallback(dir)
				} else {
					r.Static(dir)
				}

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, tt.path, nil)
				req.Header.Set("Accept", "application/json")
				if tt.header[0] != "" {
					req.Header.Set(tt.header[0], tt.header[1])
				}
				r.ServeHTTP(w, req)

				if w.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
				}
				if ct := w.Header().Get("Content-Type"); ct != "application/problem+json" {
					t.Errorf("Content-Type = %q, want application/problem+json (body %q)", ct, w.Body.String())
				}
				if got := w.Header().Get("Content-Range"); got != tt.wantRange {
					t.Errorf("Content-Range = %q, want %q", got, tt.wantRange)
				}
				if got := w.Header().Get("Cache-Control"); got != "" && tt.wantStatus != http.StatusNotFound {
					t.Errorf("Cache-Control = %q on the error answer, want the file's policy dropped", got)
				}
				failed := 0
				for _, e := range collector.getEvents() {
					if _, ok := e.(*RequestFailed); ok {
						failed++
					}
				}
				if got := int(errorLines.Load()); got != tt.wantErrors || failed != tt.wantErrors {
					t.Errorf("error log lines = %d, RequestFailed = %d, want %d each", got, failed, tt.wantErrors)
				}
			})
		}
	}
}
