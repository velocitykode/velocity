package router

import (
	"crypto/tls"
	"net/http"
	"testing"
)

func TestSecurityHeaders_Defaults(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders()
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := map[string]string{
		"X-Content-Type-Options":            "nosniff",
		"X-Frame-Options":                   "DENY",
		"Referrer-Policy":                   "strict-origin-when-cross-origin",
		"X-XSS-Protection":                  "0",
		"Permissions-Policy":                "camera=(), microphone=(), geolocation=()",
		"Content-Security-Policy":           "default-src 'self'",
		"Strict-Transport-Security":         "max-age=63072000; includeSubDomains",
		"X-Permitted-Cross-Domain-Policies": "none",
	}

	for header, want := range expected {
		got := w.Header().Get(header)
		if got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}
}

func TestSecurityHeaders_WithCSP(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithCSP("default-src 'self'; script-src 'self' cdn.example.com"))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("Content-Security-Policy")
	want := "default-src 'self'; script-src 'self' cdn.example.com"
	if got != want {
		t.Errorf("Content-Security-Policy = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_WithHSTSDisabled(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithHSTS(false))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("Strict-Transport-Security should be empty when HSTS disabled, got %q", got)
	}
}

func TestSecurityHeaders_WithHSTSMaxAge(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithHSTSMaxAge(31536000))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("Strict-Transport-Security")
	want := "max-age=31536000; includeSubDomains"
	if got != want {
		t.Errorf("Strict-Transport-Security = %q, want %q", got, want)
	}
}

func TestSecurityHeaders_WithFrameOptions(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithFrameOptions("SAMEORIGIN"))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("X-Frame-Options")
	if got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "SAMEORIGIN")
	}
}

func TestSecurityHeaders_WithReferrerPolicy(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithReferrerPolicy("no-referrer"))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("Referrer-Policy")
	if got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want %q", got, "no-referrer")
	}
}

func TestSecurityHeaders_WithCrossDomainPolicies(t *testing.T) {
	c, w := NewTestContext("GET", "/test")

	mw := SecurityHeaders(WithCrossDomainPolicies("master-only"))
	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("X-Permitted-Cross-Domain-Policies")
	if got != "master-only" {
		t.Errorf("X-Permitted-Cross-Domain-Policies = %q, want %q", got, "master-only")
	}
}

func TestHTTPSRedirect_RedirectsHTTPToHTTPS(t *testing.T) {
	c, w := NewTestContext("GET", "/dashboard")
	c.Request.Host = "example.com"

	mw := HTTPSRedirect()
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if nextCalled {
		t.Error("next handler should not be called on HTTP request")
	}
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
	}
	loc := w.Header().Get("Location")
	if loc != "https://example.com/dashboard" {
		t.Errorf("Location = %q, want %q", loc, "https://example.com/dashboard")
	}
}

func TestHTTPSRedirect_SkipsWhenTLS(t *testing.T) {
	c, _ := NewTestContext("GET", "/test")
	c.Request.TLS = &tls.ConnectionState{}

	mw := HTTPSRedirect()
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("next handler should be called for TLS requests")
	}
}

func TestHTTPSRedirect_TrustsXForwardedProtoFromTrustedProxy(t *testing.T) {
	c, _ := NewTestContext("GET", "/test")
	c.Request.RemoteAddr = "10.0.0.1:12345"
	c.Request.Header.Set("X-Forwarded-Proto", "https")

	mw := HTTPSRedirect(WithHTTPSRedirectTrustedProxies([]string{"10.0.0.1"}))
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("next handler should be called when X-Forwarded-Proto=https from trusted proxy")
	}
}

func TestHTTPSRedirect_IgnoresXForwardedProtoWithoutTrustedProxies(t *testing.T) {
	c, w := NewTestContext("GET", "/test")
	c.Request.Host = "example.com"
	c.Request.Header.Set("X-Forwarded-Proto", "https")

	mw := HTTPSRedirect()
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if nextCalled {
		t.Error("next handler should NOT be called when no trusted proxies configured")
	}
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
	}
}

func TestHTTPSRedirect_IgnoresXForwardedProtoFromUntrustedProxy(t *testing.T) {
	c, w := NewTestContext("GET", "/test")
	c.Request.Host = "example.com"
	c.Request.RemoteAddr = "192.168.1.1:12345"
	c.Request.Header.Set("X-Forwarded-Proto", "https")

	mw := HTTPSRedirect(WithHTTPSRedirectTrustedProxies([]string{"10.0.0.1"}))
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if nextCalled {
		t.Error("next handler should NOT be called when request is from untrusted proxy")
	}
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
	}
}

func TestHTTPSRedirect_ExcludesConfiguredPaths(t *testing.T) {
	mw := HTTPSRedirect(WithExcludePaths("/health", "/ready"))

	tests := []struct {
		path     string
		excluded bool
	}{
		{"/health", true},
		{"/ready", true},
		{"/page", false},
		{"/health/detail", false}, // exact match only
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			c, w := NewTestContext("GET", tt.path)
			c.Request.Host = "example.com"

			nextCalled := false
			handler := mw(func(c *Context) error {
				nextCalled = true
				return nil
			})

			if err := handler(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.excluded && !nextCalled {
				t.Errorf("path %s should be excluded from redirect", tt.path)
			}
			if !tt.excluded && nextCalled {
				t.Errorf("path %s should NOT be excluded from redirect", tt.path)
			}
			if !tt.excluded && w.Code != http.StatusMovedPermanently {
				t.Errorf("path %s: status = %d, want %d", tt.path, w.Code, http.StatusMovedPermanently)
			}
		})
	}
}

func TestHTTPSRedirect_PreservesQueryString(t *testing.T) {
	mw := HTTPSRedirect()
	c, w := NewTestContext("GET", "/search?q=test&page=2")
	c.Request.Host = "example.com"

	handler := mw(func(c *Context) error { return nil })

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loc := w.Header().Get("Location")
	if loc != "https://example.com/search?q=test&page=2" {
		t.Errorf("Location = %q, want query string preserved", loc)
	}
}

func TestHTTPSRedirect_TrustedProxyCIDR(t *testing.T) {
	mw := HTTPSRedirect(
		WithHTTPSRedirectTrustedProxies([]string{"10.0.0.0/8"}),
	)
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	c, _ := NewTestContext("GET", "/page")
	c.Request.RemoteAddr = "10.1.2.3:443"
	c.Request.Header.Set("X-Forwarded-Proto", "https")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("next handler should be called when proxy is in trusted CIDR")
	}
}

func TestHTTPSRedirect_XForwardedProtoHTTP_FromTrustedProxy(t *testing.T) {
	// Trusted proxy says proto is http — should still redirect
	mw := HTTPSRedirect(
		WithHTTPSRedirectTrustedProxies([]string{"10.0.0.1"}),
	)
	nextCalled := false
	handler := mw(func(c *Context) error {
		nextCalled = true
		return nil
	})

	c, w := NewTestContext("GET", "/page")
	c.Request.Host = "example.com"
	c.Request.RemoteAddr = "10.0.0.1:12345"
	c.Request.Header.Set("X-Forwarded-Proto", "http")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if nextCalled {
		t.Error("next handler should NOT be called when forwarded proto is http")
	}
	if w.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMovedPermanently)
	}
}

func TestSecurityHeaders_CallsNextHandler(t *testing.T) {
	called := false
	mw := SecurityHeaders()
	handler := mw(func(c *Context) error {
		called = true
		return nil
	})

	c, _ := NewTestContext("GET", "/test")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestSecurityHeaders_MultipleOptions(t *testing.T) {
	mw := SecurityHeaders(
		WithCSP("default-src 'none'"),
		WithHSTS(false),
		WithFrameOptions("SAMEORIGIN"),
		WithReferrerPolicy("no-referrer"),
	)
	c, w := NewTestContext("GET", "/test")

	if err := mw(func(c *Context) error { return nil })(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := w.Header().Get("Content-Security-Policy"); got != "default-src 'none'" {
		t.Errorf("CSP = %q, want %q", got, "default-src 'none'")
	}
	if got := w.Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS should be absent, got %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want %q", got, "SAMEORIGIN")
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want %q", got, "no-referrer")
	}
	// Unmodified headers should still have defaults
	if got := w.Header().Get("Permissions-Policy"); got != "camera=(), microphone=(), geolocation=()" {
		t.Errorf("Permissions-Policy = %q, want default", got)
	}
}

func TestSecurityHeaders_WithPermissionsPolicy(t *testing.T) {
	custom := "camera=(), microphone=()"
	mw := SecurityHeaders(WithPermissionsPolicy(custom))
	c, w := NewTestContext("GET", "/test")

	if err := mw(func(c *Context) error { return nil })(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := w.Header().Get("Permissions-Policy"); got != custom {
		t.Errorf("Permissions-Policy = %q, want %q", got, custom)
	}
}

func TestSecurityHeaders_WithHSTSIncludeSubDomains(t *testing.T) {
	mw := SecurityHeaders(WithHSTSIncludeSubDomains(false))
	c, w := NewTestContext("GET", "/test")

	if err := mw(func(c *Context) error { return nil })(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := w.Header().Get("Strict-Transport-Security")
	want := "max-age=63072000"
	if got != want {
		t.Errorf("Strict-Transport-Security = %q, want %q (no includeSubDomains)", got, want)
	}
}

func TestWithExcludePaths_NilMapSafe(t *testing.T) {
	// WithExcludePaths must not panic when applied to a config with nil excludePaths.
	// This tests the defensive nil-map initialization.
	opt := WithExcludePaths("/health", "/ready")
	cfg := &httpsRedirectConfig{} // excludePaths is nil
	opt(cfg)                      // must not panic

	if !cfg.excludePaths["/health"] {
		t.Error("expected /health in excludePaths")
	}
	if !cfg.excludePaths["/ready"] {
		t.Error("expected /ready in excludePaths")
	}
}
