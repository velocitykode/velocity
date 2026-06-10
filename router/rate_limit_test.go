package router

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Helper to create a test context
func createTestContext(method, path string, headers map[string]string) (*Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ctx := &Context{
		Request:  req,
		Response: rec,
		params:   make([]RouteParam, 0),
		values:   make(map[string]interface{}),
	}
	return ctx, rec
}

// successHandler is a simple handler that returns 200 OK
func successHandler(c *Context) error {
	return c.String(http.StatusOK, "OK")
}

func TestRateLimit_AllowsRequestsUnderLimit(t *testing.T) {
	middleware := RateLimit(5, time.Second)
	handler := middleware(successHandler)

	// Should allow 5 requests
	for i := 0; i < 5; i++ {
		ctx, rec := createTestContext("GET", "/test", nil)
		err := handler(ctx)
		if err != nil {
			t.Fatalf("Request %d: unexpected error: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected status 200, got %d", i+1, rec.Code)
		}
	}
}

func TestRateLimit_BlocksRequestsOverLimit(t *testing.T) {
	middleware := RateLimit(3, time.Second)
	handler := middleware(successHandler)

	// Make 3 allowed requests
	for i := 0; i < 3; i++ {
		ctx, _ := createTestContext("GET", "/test", nil)
		_ = handler(ctx)
	}

	// 4th request should be blocked
	ctx, rec := createTestContext("GET", "/test", nil)
	err := handler(ctx)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("Expected status 429, got %d", rec.Code)
	}
}

func TestRateLimitByIP_TracksSeparateIPs(t *testing.T) {
	middleware := RateLimitByIP(2, time.Second)
	handler := middleware(successHandler)

	// Helper to create context with specific RemoteAddr
	ctxWithIP := func(ip string) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = ip + ":12345"
		rec := httptest.NewRecorder()
		return &Context{Request: req, Response: rec, params: make([]RouteParam, 0), values: make(map[string]interface{})}, rec
	}

	// IP1 makes 2 requests (should be allowed)
	for i := 0; i < 2; i++ {
		ctx, rec := ctxWithIP("192.168.1.1")
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("IP1 request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// IP1 makes 3rd request (should be blocked)
	ctx, rec := ctxWithIP("192.168.1.1")
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("IP1 3rd request: expected 429, got %d", rec.Code)
	}

	// IP2 makes request (should be allowed - separate limit)
	ctx, rec = ctxWithIP("192.168.1.2")
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("IP2 request: expected 200, got %d", rec.Code)
	}
}

func TestRateLimitByKey_CustomKeyFunction(t *testing.T) {
	middleware := RateLimitByKey(2, time.Second, func(c *Context) string {
		return c.Header("X-API-Key")
	})
	handler := middleware(successHandler)

	// Key1 makes 2 requests (allowed)
	for i := 0; i < 2; i++ {
		ctx, rec := createTestContext("GET", "/test", map[string]string{"X-API-Key": "key1"})
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Key1 request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// Key1 makes 3rd request (blocked)
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-API-Key": "key1"})
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("Key1 3rd request: expected 429, got %d", rec.Code)
	}

	// Key2 makes request (allowed - different key)
	ctx, rec = createTestContext("GET", "/test", map[string]string{"X-API-Key": "key2"})
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Key2 request: expected 200, got %d", rec.Code)
	}
}

func TestRateLimit_RetryAfterHeader(t *testing.T) {
	middleware := RateLimit(1, 10*time.Second)
	handler := middleware(successHandler)

	// First request - allowed
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Second request - blocked
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	retryAfter := rec.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Error("Expected Retry-After header to be set")
	}

	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		t.Errorf("Retry-After should be numeric: %v", err)
	}
	if seconds < 1 || seconds > 10 {
		t.Errorf("Retry-After should be between 1 and 10, got %d", seconds)
	}
}

func TestRateLimit_XRateLimitHeaders(t *testing.T) {
	middleware := RateLimit(5, time.Minute)
	handler := middleware(successHandler)

	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Check X-RateLimit-Limit header
	limit := rec.Header().Get("X-RateLimit-Limit")
	if limit != "5" {
		t.Errorf("Expected X-RateLimit-Limit: 5, got %s", limit)
	}

	// Check X-RateLimit-Remaining header exists
	remaining := rec.Header().Get("X-RateLimit-Remaining")
	if remaining == "" {
		t.Error("Expected X-RateLimit-Remaining header to be set")
	}

	// Check X-RateLimit-Reset header
	reset := rec.Header().Get("X-RateLimit-Reset")
	if reset == "" {
		t.Error("Expected X-RateLimit-Reset header to be set")
	}
	resetTime, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		t.Errorf("X-RateLimit-Reset should be unix timestamp: %v", err)
	}
	if resetTime < time.Now().Unix() {
		t.Error("X-RateLimit-Reset should be in the future")
	}
}

func TestRateLimit_WithBurst(t *testing.T) {
	// Rate: 2 requests per second with burst of 5
	middleware := RateLimit(2, time.Second, WithBurst(5))
	handler := middleware(successHandler)

	// Should allow 5 requests in burst
	for i := 0; i < 5; i++ {
		ctx, rec := createTestContext("GET", "/test", nil)
		err := handler(ctx)
		if err != nil {
			t.Fatalf("Request %d: unexpected error: %v", i+1, err)
		}
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 6th request should be blocked (burst exhausted)
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: expected 429, got %d", rec.Code)
	}
}

func TestRateLimit_WithSkip(t *testing.T) {
	middleware := RateLimit(1, time.Second, WithSkip(func(c *Context) bool {
		return c.Path() == "/health"
	}))
	handler := middleware(successHandler)

	// First normal request - allowed
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}

	// Second normal request - blocked
	ctx, rec = createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("Expected 429, got %d", rec.Code)
	}

	// Health check - should skip rate limiting
	ctx, rec = createTestContext("GET", "/health", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Health check should bypass rate limit, got %d", rec.Code)
	}
}

func TestRateLimit_WithOnLimitReached(t *testing.T) {
	callbackCalled := false
	var callbackContext *Context

	middleware := RateLimit(1, time.Second, WithOnLimitReached(func(c *Context) {
		callbackCalled = true
		callbackContext = c
	}))
	handler := middleware(successHandler)

	// First request - allowed (callback not called)
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if callbackCalled {
		t.Error("Callback should not be called for allowed request")
	}

	// Second request - blocked (callback should be called)
	ctx, _ = createTestContext("GET", "/blocked", nil)
	_ = handler(ctx)
	if !callbackCalled {
		t.Error("Callback should be called when limit is reached")
	}
	if callbackContext == nil {
		t.Error("Callback should receive the context")
	}
}

func TestRateLimit_WithMessage(t *testing.T) {
	customMessage := "Slow down there, partner!"
	middleware := RateLimit(1, time.Second, WithMessage(customMessage))
	handler := middleware(successHandler)

	// First request - allowed
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Second request - blocked
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	if msg, ok := response["message"].(string); !ok || msg != customMessage {
		t.Errorf("Expected message %q, got %v", customMessage, response["message"])
	}
}

func TestExtractIP_XForwardedFor(t *testing.T) {
	// Trust headers only when proxy is trusted — httptest default RemoteAddr is 192.0.2.1:1234
	trusted, err := ParseTrustedProxies([]string{"192.0.2.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}

	// The rewrite now follows RFC 7239 right-most-trusted semantics:
	// with only 192.0.2.1 trusted, the right-most untrusted IP of the
	// XFF header is returned. That is the last hop, not the first.
	tests := []struct {
		name     string
		xff      string
		expected string
	}{
		{"single IP", "192.168.1.1", "192.168.1.1"},
		{"multiple IPs", "192.168.1.1, 10.0.0.1, 172.16.0.1", "172.16.0.1"},
		{"with spaces", "  192.168.1.1  ,  10.0.0.1  ", "10.0.0.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := createTestContext("GET", "/", map[string]string{"X-Forwarded-For": tt.xff})
			ip := extractIP(ctx, trusted.IPNets())
			if ip != tt.expected {
				t.Errorf("Expected IP %s, got %s", tt.expected, ip)
			}
		})
	}
}

func TestExtractIP_XForwardedFor_UntrustedProxy(t *testing.T) {
	// Without trusted proxies, headers should be ignored
	ctx, _ := createTestContext("GET", "/", map[string]string{"X-Forwarded-For": "192.168.1.1"})
	ip := extractIP(ctx, nil)
	if ip != "192.0.2.1" {
		t.Errorf("Expected RemoteAddr IP 192.0.2.1, got %s", ip)
	}
}

func TestExtractIP_XRealIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"192.0.2.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ctx, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": "10.0.0.1"})
	ip := extractIP(ctx, trusted.IPNets())
	if ip != "10.0.0.1" {
		t.Errorf("Expected IP 10.0.0.1, got %s", ip)
	}
}

func TestExtractIP_FallbackToRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	ctx := &Context{Request: req, Response: rec}

	ip := extractIP(ctx, nil)
	if ip != "127.0.0.1" {
		t.Errorf("Expected IP 127.0.0.1, got %s", ip)
	}
}

func TestExtractIP_IPv6WithPort(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::1]:8080"
	rec := httptest.NewRecorder()
	ctx := &Context{Request: req, Response: rec}

	ip := extractIP(ctx, nil)
	if ip != "::1" {
		t.Errorf("Expected IP ::1, got %s", ip)
	}
}

func TestExtractIP_XForwardedForPriority(t *testing.T) {
	// X-Forwarded-For should take priority over X-Real-IP when proxy is trusted
	trusted, err := ParseTrustedProxies([]string{"192.0.2.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ctx, _ := createTestContext("GET", "/", map[string]string{
		"X-Forwarded-For": "192.168.1.1",
		"X-Real-IP":       "10.0.0.1",
	})
	ip := extractIP(ctx, trusted.IPNets())
	if ip != "192.168.1.1" {
		t.Errorf("Expected X-Forwarded-For IP 192.168.1.1, got %s", ip)
	}
}

func TestRateLimitByKey_CleanupRemovesExpiredLimiters(t *testing.T) {
	// Use short window and cleanup interval for testing
	window := 50 * time.Millisecond
	cleanupInterval := 10 * time.Millisecond

	rlc, middleware := RateLimitByKeyWithCleanupControl(10, window, func(c *Context) string {
		return c.Header("X-API-Key")
	}, WithCleanupInterval(cleanupInterval))
	defer rlc.Stop()

	handler := middleware(successHandler)

	// Create limiters for 3 keys
	for _, key := range []string{"key1", "key2", "key3"} {
		ctx, _ := createTestContext("GET", "/test", map[string]string{"X-API-Key": key})
		_ = handler(ctx)
	}

	if rlc.Count() != 3 {
		t.Errorf("Expected 3 limiters, got %d", rlc.Count())
	}

	// Set old access times for key1 and key2
	now := time.Now()
	rlc.SetLastAccess("key1", now.Add(-window*3))
	rlc.SetLastAccess("key2", now.Add(-window*3))

	// Force cleanup
	rlc.ForceCleanup()

	// Only key3 should remain
	if rlc.Count() != 1 {
		t.Errorf("Expected 1 limiter after cleanup, got %d", rlc.Count())
	}
}

// TestRateLimitByKey_WindowStatePrunedWithLimiter verifies that per-key window
// state is stored on the limiter entry (not a parallel unbounded map) so it is
// pruned together with the limiter on cleanup, and that a re-seen key gets a
// fresh window rather than reviving stale state.
func TestRateLimitByKey_WindowStatePrunedWithLimiter(t *testing.T) {
	window := 1100 * time.Millisecond
	cleanupInterval := time.Hour // never fires on its own; we drive ForceCleanup

	rlc, middleware := RateLimitByKeyWithCleanupControl(10, window, func(c *Context) string {
		return c.Header("X-API-Key")
	}, WithCleanupInterval(cleanupInterval))
	defer rlc.Stop()

	handler := middleware(successHandler)

	// Seed window state for many keys.
	const numKeys = 50
	keys := make([]string, numKeys)
	for i := range keys {
		keys[i] = "key" + strconv.Itoa(i)
	}
	var firstReset string
	for i, key := range keys {
		ctx, rec := createTestContext("GET", "/test", map[string]string{"X-API-Key": key})
		_ = handler(ctx)
		if i == 0 {
			firstReset = rec.Header().Get("X-RateLimit-Reset")
		}
	}
	if rlc.Count() != numKeys {
		t.Fatalf("Expected %d limiters, got %d", numKeys, rlc.Count())
	}

	// Age every key so ForceCleanup removes them all.
	old := time.Now().Add(-window * 3)
	for _, key := range keys {
		rlc.SetLastAccess(key, old)
	}
	rlc.ForceCleanup()

	// With window state living on the entry, zero entries means zero leaked
	// window state: there is no second map left to grow unbounded.
	if rlc.Count() != 0 {
		t.Fatalf("Expected 0 limiters after cleanup, got %d (window state would leak in a parallel map)", rlc.Count())
	}

	// A re-seen key must get a fresh window starting now, not revive stale state.
	time.Sleep(1100 * time.Millisecond)
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-API-Key": keys[0]})
	_ = handler(ctx)
	if rlc.Count() != 1 {
		t.Fatalf("Expected 1 limiter after re-seeing key, got %d", rlc.Count())
	}
	secondReset := rec.Header().Get("X-RateLimit-Reset")
	if firstReset == "" || secondReset == "" {
		t.Fatalf("Expected reset headers to be set, got %q and %q", firstReset, secondReset)
	}
	if secondReset <= firstReset {
		t.Errorf("Re-seen key should get a fresh (later) window: first reset %s, second reset %s", firstReset, secondReset)
	}
}

func TestRateLimit_ConcurrentRequests(t *testing.T) {
	middleware := RateLimit(100, time.Second)
	handler := middleware(successHandler)

	var wg sync.WaitGroup
	var allowed, blocked int64

	// Fire 200 concurrent requests
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, rec := createTestContext("GET", "/test", nil)
			_ = handler(ctx)
			if rec.Code == http.StatusOK {
				atomic.AddInt64(&allowed, 1)
			} else if rec.Code == http.StatusTooManyRequests {
				atomic.AddInt64(&blocked, 1)
			}
		}()
	}

	wg.Wait()

	// Should have allowed around 100 and blocked around 100
	// Allow some tolerance due to race conditions
	if allowed < 95 || allowed > 105 {
		t.Errorf("Expected ~100 allowed requests, got %d", allowed)
	}
	if blocked < 95 || blocked > 105 {
		t.Errorf("Expected ~100 blocked requests, got %d", blocked)
	}
}

func TestRateLimitByIP_ConcurrentRequests(t *testing.T) {
	middleware := RateLimitByIP(10, time.Second)
	handler := middleware(successHandler)

	var wg sync.WaitGroup
	results := make(map[string]*struct{ allowed, blocked int64 })
	var mu sync.Mutex

	ips := []string{"192.168.1.1", "192.168.1.2", "192.168.1.3"}
	for _, ip := range ips {
		results[ip] = &struct{ allowed, blocked int64 }{}
	}

	// Fire 20 concurrent requests per IP using RemoteAddr
	for _, ip := range ips {
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(ipAddr string) {
				defer wg.Done()
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = ipAddr + ":12345"
				rec := httptest.NewRecorder()
				ctx := &Context{Request: req, Response: rec, params: make([]RouteParam, 0), values: make(map[string]interface{})}
				_ = handler(ctx)
				mu.Lock()
				if rec.Code == http.StatusOK {
					results[ipAddr].allowed++
				} else {
					results[ipAddr].blocked++
				}
				mu.Unlock()
			}(ip)
		}
	}

	wg.Wait()

	// Each IP should have ~10 allowed and ~10 blocked
	for ip, r := range results {
		if r.allowed < 8 || r.allowed > 12 {
			t.Errorf("IP %s: expected ~10 allowed, got %d", ip, r.allowed)
		}
		if r.blocked < 8 || r.blocked > 12 {
			t.Errorf("IP %s: expected ~10 blocked, got %d", ip, r.blocked)
		}
	}
}

func TestMultipleLimitersOnRoute(t *testing.T) {
	// Create two limiters: 5/second and 10/minute
	limiter1 := RateLimit(5, time.Second)
	limiter2 := RateLimit(10, time.Minute)

	// Stack them
	handler := limiter1(limiter2(successHandler))

	// Should allow first 5 (limited by per-second limiter)
	for i := 0; i < 5; i++ {
		ctx, rec := createTestContext("GET", "/test", nil)
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 6th request should be blocked by per-second limiter
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: expected 429, got %d", rec.Code)
	}
}

func TestNewRateLimitGroup(t *testing.T) {
	// Use a group of limiters
	group := NewRateLimitGroup(
		RateLimit(3, time.Second),
		RateLimit(5, time.Minute),
	)
	handler := group(successHandler)

	// Should allow 3 requests (limited by stricter per-second limiter)
	for i := 0; i < 3; i++ {
		ctx, rec := createTestContext("GET", "/test", nil)
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 4th request blocked
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("4th request: expected 429, got %d", rec.Code)
	}
}

func TestThrottle_Alias(t *testing.T) {
	// Throttle is just an alias for RateLimitByIP
	middleware := Throttle(2, time.Second)
	handler := middleware(successHandler)

	// Make 2 requests from same IP - should be allowed
	for i := 0; i < 2; i++ {
		ctx, rec := createTestContext("GET", "/test", map[string]string{"X-Real-IP": "1.2.3.4"})
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 3rd request blocked
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-Real-IP": "1.2.3.4"})
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: expected 429, got %d", rec.Code)
	}
}

func TestRateLimitConfig_String(t *testing.T) {
	cfg := &RateLimitConfig{
		Burst:           10,
		Message:         "Too fast!",
		CleanupInterval: 5 * time.Minute,
	}

	str := cfg.String()
	if str == "" {
		t.Error("String() should return non-empty string")
	}
}

func TestRateLimit_ResponseBody(t *testing.T) {
	middleware := RateLimit(1, time.Second)
	handler := middleware(successHandler)

	// Exhaust limit
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Get blocked response
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("Failed to parse response body: %v", err)
	}

	// Check code
	if code, ok := response["code"].(float64); !ok || int(code) != 429 {
		t.Errorf("Expected code 429, got %v", response["code"])
	}

	// Check message
	if msg, ok := response["message"].(string); !ok || msg == "" {
		t.Errorf("Expected non-empty message, got %v", response["message"])
	}
}

func TestRateLimitByIP_EmptyHeaders(t *testing.T) {
	// No X-Forwarded-For or X-Real-IP, should use RemoteAddr
	middleware := RateLimitByIP(2, time.Second)
	handler := middleware(successHandler)

	// Create request without proxy headers
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:12345"
	rec := httptest.NewRecorder()
	ctx := &Context{
		Request:  req,
		Response: rec,
		params:   make([]RouteParam, 0),
		values:   make(map[string]interface{}),
	}

	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}
}

func TestWithCleanupInterval_Option(t *testing.T) {
	customInterval := 10 * time.Second
	cfg := &RateLimitConfig{}
	WithCleanupInterval(customInterval)(cfg)

	if cfg.CleanupInterval != customInterval {
		t.Errorf("Expected cleanup interval %v, got %v", customInterval, cfg.CleanupInterval)
	}
}

func TestRateLimitByKey_WindowReset(t *testing.T) {
	window := 100 * time.Millisecond
	middleware := RateLimitByKey(2, window, func(c *Context) string {
		return "test-key"
	})
	handler := middleware(successHandler)

	// Make 2 requests (exhausts limit)
	for i := 0; i < 2; i++ {
		ctx, rec := createTestContext("GET", "/test", nil)
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 3rd request blocked
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: expected 429, got %d", rec.Code)
	}

	// Wait for window to reset
	time.Sleep(window + 50*time.Millisecond)

	// Should allow requests again (tokens replenished)
	ctx, rec = createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("After window reset: expected 200, got %d", rec.Code)
	}
}

func TestRateLimit_ZeroBurst(t *testing.T) {
	// Burst of 0 means no burst allowed
	middleware := RateLimit(10, time.Second, WithBurst(0))
	handler := middleware(successHandler)

	// With burst 0, no requests should be allowed immediately
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("With zero burst, expected 429, got %d", rec.Code)
	}
}

func TestRateLimitByIP_MultipleOptionsComposed(t *testing.T) {
	callbackCount := 0
	middleware := RateLimitByIP(2, time.Second,
		WithBurst(2),
		WithMessage("Custom limit message"),
		WithSkip(func(c *Context) bool {
			return c.Header("X-Skip") == "true"
		}),
		WithOnLimitReached(func(c *Context) {
			callbackCount++
		}),
		WithCleanupInterval(time.Minute),
	)
	handler := middleware(successHandler)

	// Make 2 requests
	for i := 0; i < 2; i++ {
		ctx, _ := createTestContext("GET", "/test", map[string]string{"X-Real-IP": "1.1.1.1"})
		_ = handler(ctx)
	}

	// 3rd request should trigger callback and use custom message
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-Real-IP": "1.1.1.1"})
	_ = handler(ctx)

	if callbackCount != 1 {
		t.Errorf("Expected callback to be called once, got %d", callbackCount)
	}

	var response map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &response)
	if msg := response["message"]; msg != "Custom limit message" {
		t.Errorf("Expected custom message, got %v", msg)
	}

	// Skip header should bypass
	ctx, rec = createTestContext("GET", "/test", map[string]string{
		"X-Real-IP": "1.1.1.1",
		"X-Skip":    "true",
	})
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Skip should bypass rate limit, got %d", rec.Code)
	}
}

// BenchmarkRateLimit measures overhead of rate limiting
func BenchmarkRateLimit(b *testing.B) {
	middleware := RateLimit(1000000, time.Second) // High limit to not block
	handler := middleware(successHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, _ := createTestContext("GET", "/test", nil)
		_ = handler(ctx)
	}
}

// BenchmarkRateLimitByIP measures per-IP rate limiter overhead
func BenchmarkRateLimitByIP(b *testing.B) {
	middleware := RateLimitByIP(1000000, time.Second)
	handler := middleware(successHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, _ := createTestContext("GET", "/test", map[string]string{"X-Real-IP": "192.168.1.1"})
		_ = handler(ctx)
	}
}

// BenchmarkRateLimitByIP_ManyIPs measures performance with many different IPs
func BenchmarkRateLimitByIP_ManyIPs(b *testing.B) {
	middleware := RateLimitByIP(1000000, time.Second)
	handler := middleware(successHandler)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ip := "192.168." + strconv.Itoa(i%256) + "." + strconv.Itoa((i/256)%256)
		ctx, _ := createTestContext("GET", "/test", map[string]string{"X-Real-IP": ip})
		_ = handler(ctx)
	}
}

// mockRateLimitStore implements RateLimitStore for testing
type mockRateLimitStore struct {
	mu        sync.Mutex
	counts    map[string]int
	limit     int
	resetTime time.Time
}

func newMockStore(limit int) *mockRateLimitStore {
	return &mockRateLimitStore{
		counts:    make(map[string]int),
		limit:     limit,
		resetTime: time.Now().Add(time.Minute),
	}
}

func (m *mockRateLimitStore) Allow(key string) (allowed bool, remaining int, resetTime time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counts[key]++
	remaining = m.limit - m.counts[key]
	if remaining < 0 {
		remaining = 0
	}
	return m.counts[key] <= m.limit, remaining, m.resetTime
}

func TestRateLimitWithStore(t *testing.T) {
	store := newMockStore(2)
	middleware := RateLimitWithStore(store, func(c *Context) string {
		return c.Header("X-User-ID")
	})
	handler := middleware(successHandler)

	// First two requests should be allowed
	for i := 0; i < 2; i++ {
		ctx, rec := createTestContext("GET", "/test", map[string]string{"X-User-ID": "user1"})
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// Third request should be blocked
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-User-ID": "user1"})
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request: expected 429, got %d", rec.Code)
	}
}

func TestRateLimitWithStore_Skip(t *testing.T) {
	store := newMockStore(1)
	middleware := RateLimitWithStore(store, func(c *Context) string {
		return "key"
	}, WithSkip(func(c *Context) bool {
		return c.Path() == "/skip"
	}))
	handler := middleware(successHandler)

	// First request uses the limit
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}

	// Second request to /skip should bypass
	ctx, rec = createTestContext("GET", "/skip", nil)
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Skip path should bypass, got %d", rec.Code)
	}
}

func TestRateLimitWithStore_OnLimitReached(t *testing.T) {
	store := newMockStore(1)
	called := false
	middleware := RateLimitWithStore(store, func(c *Context) string {
		return "key"
	}, WithOnLimitReached(func(c *Context) {
		called = true
	}))
	handler := middleware(successHandler)

	// Exhaust limit
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Trigger callback
	ctx, _ = createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	if !called {
		t.Error("OnLimitReached callback should be called")
	}
}

func TestThrottleByKey(t *testing.T) {
	middleware := ThrottleByKey(2, time.Second, func(c *Context) string {
		return c.Header("X-Tenant")
	})
	handler := middleware(successHandler)

	// Tenant1 makes 2 requests
	for i := 0; i < 2; i++ {
		ctx, rec := createTestContext("GET", "/test", map[string]string{"X-Tenant": "tenant1"})
		_ = handler(ctx)
		if rec.Code != http.StatusOK {
			t.Errorf("Tenant1 request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// Tenant1 blocked
	ctx, rec := createTestContext("GET", "/test", map[string]string{"X-Tenant": "tenant1"})
	_ = handler(ctx)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("Tenant1 3rd request: expected 429, got %d", rec.Code)
	}

	// Tenant2 allowed
	ctx, rec = createTestContext("GET", "/test", map[string]string{"X-Tenant": "tenant2"})
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Errorf("Tenant2 request: expected 200, got %d", rec.Code)
	}
}

func TestExtractIP_IPv6NoPort(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "::1"
	rec := httptest.NewRecorder()
	ctx := &Context{Request: req, Response: rec}

	ip := extractIP(ctx, nil)
	if ip != "::1" {
		t.Errorf("Expected IP ::1, got %s", ip)
	}
}

func TestRateLimit_HeadersOnBlockedRequest(t *testing.T) {
	middleware := RateLimit(1, time.Second)
	handler := middleware(successHandler)

	// Exhaust limit
	ctx, _ := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	// Check headers on blocked request
	ctx, rec := createTestContext("GET", "/test", nil)
	_ = handler(ctx)

	if rec.Header().Get("X-RateLimit-Limit") != "1" {
		t.Error("Expected X-RateLimit-Limit: 1")
	}
	if rec.Header().Get("X-RateLimit-Remaining") != "0" {
		t.Error("Expected X-RateLimit-Remaining: 0")
	}
	if rec.Header().Get("X-RateLimit-Reset") == "" {
		t.Error("Expected X-RateLimit-Reset to be set")
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("Expected Retry-After to be set")
	}
}

// TestExtractIP_RejectsCommaInjectedXRealIP pins the regression for the
// bug where X-Real-IP was accepted without sanitisation while
// X-Forwarded-For was strictly parsed. A client that gets past the
// trusted-proxy check (or an attacker inside the trust boundary) could
// send "X-Real-IP: 1.2.3.4, 5.6.7.8" to spoof the throttle key. We now
// reject anything that isn't a single well-formed IP and fall back to
// the direct-connection address.
func TestExtractIP_RejectsCommaInjectedXRealIP(t *testing.T) {
	trusted, err := ParseTrustedProxies([]string{"192.0.2.1"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}

	tests := []struct {
		name   string
		header string
	}{
		{"comma separated", "1.2.3.4, 5.6.7.8"},
		{"comma with no space", "1.2.3.4,5.6.7.8"},
		{"trailing comma", "1.2.3.4,"},
		{"whitespace separated", "1.2.3.4 5.6.7.8"},
		{"tab separated", "1.2.3.4\t5.6.7.8"},
		{"not an ip", "not-an-ip"},
		{"empty after trim", "   "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": tt.header})
			ip := extractIP(ctx, trusted.IPNets())
			// Spoof was ignored — fell back to RemoteAddr (httptest default 192.0.2.1).
			if ip != "192.0.2.1" {
				t.Errorf("expected fallback to remoteAddr 192.0.2.1, got %q (spoofed header was honoured)", ip)
			}
		})
	}

	// Sanity: a valid single IP is still accepted.
	ctx, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": "10.0.0.9"})
	if ip := extractIP(ctx, trusted.IPNets()); ip != "10.0.0.9" {
		t.Errorf("valid single IP rejected: got %q, want 10.0.0.9", ip)
	}
}

// TestExtractIP_IgnoresXRealIPFromUntrustedPeer confirms that X-Real-IP is
// still ignored entirely when the direct peer is not a trusted proxy —
// the sanitisation is additive to the existing trust check, not a
// replacement for it.
func TestExtractIP_IgnoresXRealIPFromUntrustedPeer(t *testing.T) {
	ctx, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": "10.0.0.1"})
	// Untrusted: either nil or a different CIDR.
	ip := extractIP(ctx, nil)
	if ip != "192.0.2.1" {
		t.Errorf("untrusted peer: expected RemoteAddr 192.0.2.1, got %q", ip)
	}

	// Now with a trusted set that does NOT include httptest's default peer.
	otherTrusted, err := ParseTrustedProxies([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("ParseTrustedProxies: %v", err)
	}
	ctx2, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": "203.0.113.9"})
	if ip := extractIP(ctx2, otherTrusted.IPNets()); ip != "192.0.2.1" {
		t.Errorf("peer outside trust: expected RemoteAddr 192.0.2.1, got %q", ip)
	}
}

// TestRateLimitByIP_HonorsRouterLevelTrustedProxies asserts the C-05
// follow-up wiring: a deployment behind a load balancer sets the
// trusted-proxy list at the ROUTER level (Router.TrustedProxies),
// and RateLimitByIP picks it up via Context.TrustedProxyNets()
// without needing a per-middleware WithTrustedProxies. Distinct
// real clients behind the LB get distinct rate-limit buckets;
// without the wiring they would share one bucket per LB IP.
func TestRateLimitByIP_HonorsRouterLevelTrustedProxies(t *testing.T) {
	// Build a router with the LB CIDR in its trust list.
	r := NewV2()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	parsed := r.trustedProxiesOrParse()

	// Build the middleware with NO per-middleware list (must rely on
	// router-level). Burst=1 so the second hit from the same client
	// is denied.
	middleware := RateLimitByIP(1, time.Minute)
	handler := middleware(successHandler)

	// Helper: build a context with router-level trust populated, a
	// fixed LB RemoteAddr, and a chosen real-client XFF.
	ctxAs := func(realClient string) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:443" // LB IP
		req.Header.Set("X-Forwarded-For", realClient)
		rec := httptest.NewRecorder()
		ctx := &Context{
			Request:        req,
			Response:       rec,
			params:         make([]RouteParam, 0),
			values:         make(map[string]interface{}),
			trustedProxies: parsed,
		}
		return ctx, rec
	}

	// Client A: first request allowed.
	ctxA1, recA1 := ctxAs("203.0.113.9")
	_ = handler(ctxA1)
	if recA1.Code != http.StatusOK {
		t.Fatalf("client A first request: expected 200, got %d", recA1.Code)
	}

	// Client A: second request blocked (same bucket).
	ctxA2, recA2 := ctxAs("203.0.113.9")
	_ = handler(ctxA2)
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second request: expected 429 (same client), got %d", recA2.Code)
	}

	// Client B (different real-client IP behind the same LB): allowed.
	// If the trust list were NOT honoured, both clients would share
	// the LB's bucket and this would also be 429.
	ctxB, recB := ctxAs("198.51.100.42")
	_ = handler(ctxB)
	if recB.Code != http.StatusOK {
		t.Fatalf("client B first request: expected 200 (distinct client behind same LB), got %d (router-level trust list not honoured)", recB.Code)
	}
}

// TestRateLimitByIP_UnionsRouterAndPerMiddlewareTrust asserts the
// per-middleware WithTrustedProxies list is ADDED to the router-level
// list, not replacing it.
func TestRateLimitByIP_UnionsRouterAndPerMiddlewareTrust(t *testing.T) {
	// Router trusts 10.0.0.0/8 (the LB tier).
	r := NewV2()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	parsed := r.trustedProxiesOrParse()

	// Per-middleware adds 192.168.0.0/16 (a second hop the router
	// itself does not trust, e.g. an internal LB layer).
	middleware := RateLimitByIP(1, time.Minute, WithTrustedProxies([]string{"192.168.0.0/16"}))
	handler := middleware(successHandler)

	// Request through the chain: real client + 192.168.x (inner LB,
	// per-middleware trust) + 10.0.0.x (outer LB, router trust) ->
	// RemoteAddr. With union, right-most-of-trusted resolution
	// returns the real client.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443" // outer LB
	req.Header.Set("X-Forwarded-For", "203.0.113.9, 192.168.1.5, 10.0.0.2")
	rec := httptest.NewRecorder()
	ctx := &Context{
		Request:        req,
		Response:       rec,
		params:         make([]RouteParam, 0),
		values:         make(map[string]interface{}),
		trustedProxies: parsed,
	}
	_ = handler(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request expected 200, got %d", rec.Code)
	}

	// Second request from the same real client must hit the bucket
	// (proving the key was the real client, not the LB).
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.RemoteAddr = "10.0.0.1:443"
	req2.Header.Set("X-Forwarded-For", "203.0.113.9, 192.168.1.5, 10.0.0.2")
	rec2 := httptest.NewRecorder()
	ctx2 := &Context{
		Request:        req2,
		Response:       rec2,
		params:         make([]RouteParam, 0),
		values:         make(map[string]interface{}),
		trustedProxies: parsed,
	}
	_ = handler(ctx2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429 (same real client), got %d (union of trust lists is broken)", rec2.Code)
	}
}

// TestRouterThrottleByIP_SnapshotIsolatedFromLaterMutation asserts the
// H-13 deep-clone guarantee: ThrottleByIP captures the router's
// TrustedProxies at registration time, so mutating Router.TrustedProxies
// (or re-running ValidateConfig with a different list) afterwards does
// NOT change the bucket key for an already-registered limiter. This
// closes the foot-gun where a deployment swaps its trusted-proxy
// configuration after routes are wired and silently re-partitions
// throttling.
func TestRouterThrottleByIP_SnapshotIsolatedFromLaterMutation(t *testing.T) {
	r := NewV2()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}

	// Register the limiter NOW. The snapshot is 10.0.0.0/8.
	middleware := r.ThrottleByIP(1, time.Minute)
	handler := middleware(successHandler)

	// Mutate the router's trusted-proxy set AFTER registration. The
	// already-captured snapshot must not be affected.
	r.TrustedProxies = []string{"192.168.0.0/16"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig (post-mutation): %v", err)
	}

	// Build a request through 10.0.0.0/8 (the SNAPSHOT trust list).
	// If the middleware re-reads the router list at request time it
	// would no longer trust 10.0.0.1 and would resolve to the LB IP,
	// collapsing all clients into one bucket. With the snapshot, the
	// real client wins.
	makeReq := func(realClient string) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.1:443"
		req.Header.Set("X-Forwarded-For", realClient)
		rec := httptest.NewRecorder()
		// trustedProxies on Context reflects POST-mutation state to
		// prove ThrottleByIP does NOT consult it.
		ctx := &Context{
			Request:        req,
			Response:       rec,
			params:         make([]RouteParam, 0),
			values:         make(map[string]interface{}),
			trustedProxies: r.trustedProxiesOrParse(),
		}
		return ctx, rec
	}

	ctxA1, recA1 := makeReq("203.0.113.9")
	_ = handler(ctxA1)
	if recA1.Code != http.StatusOK {
		t.Fatalf("client A first request: expected 200, got %d", recA1.Code)
	}

	ctxA2, recA2 := makeReq("203.0.113.9")
	_ = handler(ctxA2)
	if recA2.Code != http.StatusTooManyRequests {
		t.Fatalf("client A second request: expected 429 (same client, snapshot trust), got %d", recA2.Code)
	}

	// Distinct real client behind the same LB: allowed. Proves the
	// snapshot is still resolving the LB as trusted and honouring
	// XFF for real-client distinction.
	ctxB, recB := makeReq("198.51.100.42")
	_ = handler(ctxB)
	if recB.Code != http.StatusOK {
		t.Fatalf("client B first request: expected 200 (distinct real client), got %d (snapshot lost)", recB.Code)
	}
}

// TestRouterThrottleByIP_SpoofedXFFResolvesOnlyWithTrust asserts the
// spec's other invariant: with the trusted-proxy set populated, an
// attacker who prepends a spoofed IP in front of the trusted-proxy
// hop is correctly resolved to the spoofed left-most (because the
// right-most trusted hop is skipped). Without trust, the spoof is
// ignored and the LB IP is used.
func TestRouterThrottleByIP_SpoofedXFFResolvesOnlyWithTrust(t *testing.T) {
	// Case 1: WITH trust list.
	r := NewV2()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
	withTrust := r.ThrottleByIP(1, time.Minute)(successHandler)

	makeReq := func(remote string, xff string, tp *TrustedProxies) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		ctx := &Context{
			Request:        req,
			Response:       rec,
			params:         make([]RouteParam, 0),
			values:         make(map[string]interface{}),
			trustedProxies: tp,
		}
		return ctx, rec
	}

	// First request: peer is trusted LB (10.0.0.1), XFF is "spoofed,
	// 10.0.0.2". Right-most-of-trusted resolves to "spoofed" because
	// 10.0.0.2 is trusted and gets skipped.
	ctx1, rec1 := makeReq("10.0.0.1:443", "203.0.113.9, 10.0.0.2", r.trustedProxiesOrParse())
	_ = withTrust(ctx1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("trusted: first request from spoofed expected 200, got %d", rec1.Code)
	}
	ctx2, rec2 := makeReq("10.0.0.1:443", "203.0.113.9, 10.0.0.2", r.trustedProxiesOrParse())
	_ = withTrust(ctx2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("trusted: second from same spoofed expected 429 (same bucket), got %d", rec2.Code)
	}

	// Case 2: NO trust list (fresh router). Same XFF, same LB peer:
	// header is ignored, peer IP is the bucket. Two requests from the
	// SAME LB peer with different "spoofed" entries share the bucket.
	rNoTrust := NewV2()
	if err := rNoTrust.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig (no trust): %v", err)
	}
	noTrust := rNoTrust.ThrottleByIP(1, time.Minute)(successHandler)

	ctx3, rec3 := makeReq("10.0.0.1:443", "203.0.113.9, 10.0.0.2", rNoTrust.trustedProxiesOrParse())
	_ = noTrust(ctx3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("no-trust: first request expected 200, got %d", rec3.Code)
	}
	ctx4, rec4 := makeReq("10.0.0.1:443", "198.51.100.42, 10.0.0.2", rNoTrust.trustedProxiesOrParse())
	_ = noTrust(ctx4)
	if rec4.Code != http.StatusTooManyRequests {
		t.Fatalf("no-trust: second from same LB IP expected 429 (XFF ignored), got %d", rec4.Code)
	}
}

// TestRouterThrottleByIP_UnionsPerMiddlewareTrust asserts that the
// optional WithTrustedProxies passed to Router.ThrottleByIP is unioned
// with the snapshot at registration, not silently dropped.
func TestRouterThrottleByIP_UnionsPerMiddlewareTrust(t *testing.T) {
	r := NewV2()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}

	middleware := r.ThrottleByIP(1, time.Minute, WithTrustedProxies([]string{"192.168.0.0/16"}))
	handler := middleware(successHandler)

	makeReq := func(remote, xff string) (*Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		ctx := &Context{
			Request:        req,
			Response:       rec,
			params:         make([]RouteParam, 0),
			values:         make(map[string]interface{}),
			trustedProxies: r.trustedProxiesOrParse(),
		}
		return ctx, rec
	}

	// Real client -> inner LB (192.168.x) -> outer LB (10.0.0.x) -> us.
	// Both inner and outer must be trusted for the real client to win.
	ctx1, rec1 := makeReq("10.0.0.1:443", "203.0.113.9, 192.168.1.5, 10.0.0.2")
	_ = handler(ctx1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("union: first request expected 200, got %d", rec1.Code)
	}
	ctx2, rec2 := makeReq("10.0.0.1:443", "203.0.113.9, 192.168.1.5, 10.0.0.2")
	_ = handler(ctx2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("union: second request expected 429 (same real client), got %d", rec2.Code)
	}
}

// TestRateLimitByIP_WarnsOnPrivatePeerWithNoTrust asserts the H-13
// runtime-warning behaviour: when the standalone RateLimitByIP runs
// with no trusted proxies AND the request peer is RFC1918/loopback
// (the canonical misconfigured-LB signature), the middleware logs a
// one-shot warning. The warning fires at most once per middleware
// instance to avoid log flooding.
func TestRateLimitByIP_WarnsOnPrivatePeerWithNoTrust(t *testing.T) {
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()

	middleware := RateLimitByIP(10, time.Minute)
	handler := middleware(successHandler)

	makeReq := func(remote string) *Context {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		return &Context{
			Request:  req,
			Response: rec,
			params:   make([]RouteParam, 0),
			values:   make(map[string]interface{}),
		}
	}

	// First RFC1918 peer: warning expected.
	_ = handler(makeReq("10.0.0.1:443"))
	if !strings.Contains(buf.String(), "RFC1918/loopback") {
		t.Errorf("expected warning on first private peer, got log: %q", buf.String())
	}

	// Second RFC1918 peer: warning must NOT repeat.
	buf.Reset()
	_ = handler(makeReq("192.168.1.5:443"))
	if buf.Len() != 0 {
		t.Errorf("warning fired again, log: %q", buf.String())
	}

	// Public peer on a different middleware instance: no warning.
	buf.Reset()
	mw2 := RateLimitByIP(10, time.Minute)(successHandler)
	_ = mw2(makeReq("203.0.113.9:443"))
	if buf.Len() != 0 {
		t.Errorf("public peer should not warn, log: %q", buf.String())
	}
}
