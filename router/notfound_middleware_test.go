package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestRateLimitAppliesTo404 verifies that a global RateLimit registered
// via Router.Use(...) throttles requests that hit unknown paths. Before
// the fix for security-audit-2026-05 finding E-01, the 404 path
// bypassed the global middleware chain entirely, letting an attacker
// hammer arbitrary nonexistent paths at zero throttle cost.
func TestRateLimitAppliesTo404(t *testing.T) {
	r := New()
	r.Use(RateLimit(1, time.Minute))
	r.Get("/ok", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// First request to an unknown path: 404 (route not matched, but
	// the synthetic terminal handler still consumes one token).
	rec1 := httptest.NewRecorder()
	r.ServeHTTP(rec1, httptest.NewRequest("GET", "/no-such-path", nil))
	if rec1.Code != http.StatusNotFound {
		t.Fatalf("first request: expected 404, got %d", rec1.Code)
	}

	// Second request to a different unknown path: bucket is now empty,
	// rate limiter denies with 429. Pre-fix, this would also return 404.
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, httptest.NewRequest("GET", "/another-missing", nil))
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: expected 429 (rate limit applied to 404), got %d", rec2.Code)
	}
}

// TestSecurityHeadersAppliedTo404 verifies that global SecurityHeaders
// middleware decorates 404 responses. Pre-fix, error responses for
// unknown paths leaked without the configured headers.
func TestSecurityHeadersAppliedTo404(t *testing.T) {
	r := New()
	r.Use(SecurityHeaders(
		WithCSP("default-src 'none'"),
		WithFrameOptions("DENY"),
	))
	r.Get("/ok", func(c *Context) error { return nil })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/no-such-path", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	wantHeaders := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'",
	}
	for h, want := range wantHeaders {
		got := rec.Header().Get(h)
		if got != want {
			t.Errorf("header %s on 404 = %q, want %q", h, got, want)
		}
	}
}

// TestBodyLimitAppliedTo404 verifies that global BodyLimit middleware
// runs on the 404 path. We confirm by registering an inspector
// middleware after BodyLimit that reads the request body: a body
// larger than the limit must surface an error when read, proving the
// MaxBytesReader wrapper is in place.
func TestBodyLimitAppliedTo404(t *testing.T) {
	r := New()
	var (
		readErr error
		readN   int
	)
	// Order: BodyLimit wraps body, then inspector tries to drain it.
	r.Use(BodyLimit(10))
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			buf := make([]byte, 1024)
			n, err := io.ReadFull(c.Request.Body, buf)
			if err == io.ErrUnexpectedEOF || err == io.EOF {
				err = nil
			}
			readErr = err
			readN = n
			return next(c)
		}
	})
	r.Get("/ok", func(c *Context) error { return nil })

	// POST 200 bytes to a missing path. BodyLimit(10) must cap reads.
	body := strings.Repeat("x", 200)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/no-such-path", strings.NewReader(body))
	r.ServeHTTP(rec, req)

	if readErr == nil {
		t.Fatalf("expected body read to error past the 10-byte limit (BodyLimit middleware bypassed on 404); read %d bytes with nil err", readN)
	}
	// Should read up to ~10 bytes before MaxBytesReader returns an error.
	if readN > 11 {
		t.Errorf("read past body limit: read %d bytes (expected <=11)", readN)
	}
}

// TestMiddlewareOrderingOn404 verifies the global middleware chain
// executes in registration order on the 404 path (same ordering as
// matched routes).
func TestMiddlewareOrderingOn404(t *testing.T) {
	r := New()
	var (
		mu       sync.Mutex
		executed []string
	)
	record := func(label string) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				mu.Lock()
				executed = append(executed, label+"-before")
				mu.Unlock()
				err := next(c)
				mu.Lock()
				executed = append(executed, label+"-after")
				mu.Unlock()
				return err
			}
		}
	}
	r.Use(record("a"), record("b"), record("c"))
	r.Get("/ok", func(c *Context) error { return nil })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/no-such-path", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	want := []string{
		"a-before",
		"b-before",
		"c-before",
		"c-after",
		"b-after",
		"a-after",
	}
	if len(executed) != len(want) {
		t.Fatalf("middleware execution count = %d, want %d (got %v)", len(executed), len(want), executed)
	}
	for i, exp := range want {
		if executed[i] != exp {
			t.Errorf("execution[%d] = %q, want %q", i, executed[i], exp)
		}
	}
}

// TestNotFoundBodyPreserved verifies the standard 404 response shape
// (status + body) is preserved when the chain has no middleware.
func TestNotFoundBodyPreserved(t *testing.T) {
	r := New()
	r.Get("/ok", func(c *Context) error { return nil })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/no-such-path", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "404 page not found") {
		t.Errorf("expected default 404 body, got %q", rec.Body.String())
	}
}
