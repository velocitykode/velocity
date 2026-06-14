package router

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultCORSConfig_EmptyOrigins(t *testing.T) {
	cfg := DefaultCORSConfig()

	if len(cfg.AllowedOrigins) != 0 {
		t.Errorf("expected empty AllowedOrigins, got %v", cfg.AllowedOrigins)
	}
}

func TestInsecureAllowAllCORS_WildcardOrigins(t *testing.T) {
	cfg := InsecureAllowAllCORS()

	if len(cfg.AllowedOrigins) != 1 || cfg.AllowedOrigins[0] != "*" {
		t.Errorf("expected AllowedOrigins [\"*\"], got %v", cfg.AllowedOrigins)
	}
}

func TestCORS_DefaultConfig_RejectsCrossOrigin(t *testing.T) {
	middleware := CORS(DefaultCORSConfig())

	nextCalled := false
	handler := middleware(func(c *Context) error {
		nextCalled = true
		return nil
	})

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("expected next handler to be called")
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin header, got %q", got)
	}
}

func TestCORS_PermissiveConfig_AllowsAllOrigins(t *testing.T) {
	middleware := CORS(InsecureAllowAllCORS())

	handler := middleware(func(c *Context) error {
		return nil
	})

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("expected Access-Control-Allow-Origin \"*\", got %q", got)
	}
}

func TestCORS_PermissiveConfig_Preflight(t *testing.T) {
	middleware := CORS(InsecureAllowAllCORS())

	handler := middleware(func(c *Context) error {
		t.Error("next handler should not be called for preflight")
		return nil
	})

	ctx, rec := NewTestContext("OPTIONS", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected status 204, got %d", rec.Code)
	}

	if got := rec.Header().Get("Access-Control-Allow-Methods"); got == "" {
		t.Error("expected Access-Control-Allow-Methods header to be set")
	}

	if got := rec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Error("expected Access-Control-Allow-Headers header to be set")
	}

	if got := rec.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Error("expected Access-Control-Max-Age header to be set")
	}
}

func TestCORS_SpecificOrigins(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
	}
	middleware := CORS(cfg)

	handler := middleware(func(c *Context) error {
		return nil
	})

	// Allowed origin gets headers
	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("expected Access-Control-Allow-Origin %q, got %q", "https://example.com", got)
	}

	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin, got %q", got)
	}

	// Disallowed origin gets no headers
	ctx2, rec2 := NewTestContext("GET", "/test")
	ctx2.Request.Header.Set("Origin", "https://evil.com")

	if err := handler(ctx2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin header for disallowed origin, got %q", got)
	}
}

func TestCORS_NoOriginHeader_PassesThrough(t *testing.T) {
	middleware := CORS(InsecureAllowAllCORS())

	nextCalled := false
	handler := middleware(func(c *Context) error {
		nextCalled = true
		return nil
	})

	ctx, rec := NewTestContext("GET", "/test")
	// No Origin header set

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("expected next handler to be called")
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no CORS headers, got Access-Control-Allow-Origin: %q", got)
	}
}

func TestCORS_AllowCredentials(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Content-Type"},
		AllowCredentials: true,
	}
	middleware := CORS(cfg)

	handler := middleware(func(c *Context) error {
		return nil
	})

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("expected Access-Control-Allow-Credentials: true, got %q", got)
	}

	// With credentials, origin should be echoed back (not *)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("expected origin to be echoed back, got %q", got)
	}

	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("expected Vary: Origin with credentials, got %q", got)
	}
}

func TestCORS_AllowCredentials_Preflight(t *testing.T) {
	cfg := CORSConfig{
		AllowedOrigins:   []string{"https://example.com"},
		AllowedMethods:   []string{"GET", "POST"},
		AllowedHeaders:   []string{"Authorization"},
		AllowCredentials: true,
	}
	handler := CORS(cfg)(func(c *Context) error { return nil })

	ctx, rec := NewTestContext("OPTIONS", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
}

func TestCORS_ExposedHeaders(t *testing.T) {
	cfg := InsecureAllowAllCORS()
	cfg.ExposedHeaders = []string{"X-Custom-Header", "X-Request-Id"}
	handler := CORS(cfg)(func(c *Context) error { return nil })

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := "X-Custom-Header, X-Request-Id"
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != want {
		t.Errorf("Access-Control-Expose-Headers = %q, want %q", got, want)
	}
}

func TestCORS_DefaultConfig_RejectsPreflight(t *testing.T) {
	handler := CORS(DefaultCORSConfig())(func(c *Context) error { return nil })

	ctx, rec := NewTestContext("OPTIONS", "/test")
	ctx.Request.Header.Set("Origin", "https://evil.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods should be empty for rejected origin, got %q", got)
	}
}

func TestCORS_WildcardWithCredentials_EchoesOrigin(t *testing.T) {
	// When AllowedOrigins is ["*"] with AllowCredentials, the middleware cannot
	// send "Access-Control-Allow-Origin: *" (browsers reject it with credentials).
	// Instead it echoes the request origin, effectively allowing any origin with cookies.
	cfg := InsecureAllowAllCORS()
	cfg.AllowCredentials = true
	handler := CORS(cfg)(func(c *Context) error { return nil })

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://attacker.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Origin should be echoed back (not "*") when credentials are enabled
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://attacker.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want echoed origin", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want %q", got, "true")
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q (must vary by origin when echoing)", got, "Origin")
	}
}

func TestCORS_DisallowedOrigin_PreflightShortCircuits(t *testing.T) {
	// A disallowed-origin OPTIONS preflight must not fall through to app
	// handlers or 404 logic. It should return 204 with no CORS grant headers.
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
	}
	handler := CORS(cfg)(func(c *Context) error {
		t.Error("next handler should not be called for disallowed preflight")
		return nil
	})

	ctx, rec := NewTestContext("OPTIONS", "/test")
	ctx.Request.Header.Set("Origin", "https://evil.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for disallowed preflight", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "" {
		t.Errorf("Access-Control-Allow-Methods = %q, want empty for disallowed preflight", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "" {
		t.Errorf("Access-Control-Allow-Headers = %q, want empty for disallowed preflight", got)
	}
}

func TestCORS_DisallowedOrigin_NonPreflightFallsThrough(t *testing.T) {
	// A disallowed-origin non-OPTIONS request must still fall through to the
	// app handler unchanged (the browser blocks the response because no ACAO
	// header is present).
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
	}

	nextCalled := false
	handler := CORS(cfg)(func(c *Context) error {
		nextCalled = true
		return nil
	})

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://evil.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !nextCalled {
		t.Error("expected next handler to be called for disallowed non-preflight request")
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
}

func TestCORS_AllowedOrigin_PreflightUnchanged(t *testing.T) {
	// An allowed-origin preflight should still receive the full set of CORS
	// grant headers and a 204 response.
	cfg := CORSConfig{
		AllowedOrigins: []string{"https://example.com"},
		AllowedMethods: []string{"GET", "POST"},
		AllowedHeaders: []string{"Content-Type"},
		MaxAge:         time.Hour,
	}
	handler := CORS(cfg)(func(c *Context) error {
		t.Error("next handler should not be called for preflight")
		return nil
	})

	ctx, rec := NewTestContext("OPTIONS", "/test")
	ctx.Request.Header.Set("Origin", "https://example.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://example.com")
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); got != "GET, POST" {
		t.Errorf("Access-Control-Allow-Methods = %q, want %q", got, "GET, POST")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "Content-Type" {
		t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, "Content-Type")
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got == "" {
		t.Error("expected Access-Control-Max-Age header to be set")
	}
	if got := rec.Header().Get("Vary"); got != "Origin" {
		t.Errorf("Vary = %q, want %q", got, "Origin")
	}
}

func TestCORS_MultipleOrigins_SecondMatches(t *testing.T) {
	cfg := DefaultCORSConfig()
	cfg.AllowedOrigins = []string{"https://one.com", "https://two.com"}
	handler := CORS(cfg)(func(c *Context) error { return nil })

	ctx, rec := NewTestContext("GET", "/test")
	ctx.Request.Header.Set("Origin", "https://two.com")

	if err := handler(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://two.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, "https://two.com")
	}
}
