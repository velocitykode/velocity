package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		params:   make(map[string]string),
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
		return &Context{Request: req, Response: rec, params: make(map[string]string), values: make(map[string]interface{})}, rec
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
	trusted := parseTrustedProxies([]string{"192.0.2.1"})

	tests := []struct {
		name     string
		xff      string
		expected string
	}{
		{"single IP", "192.168.1.1", "192.168.1.1"},
		{"multiple IPs", "192.168.1.1, 10.0.0.1, 172.16.0.1", "192.168.1.1"},
		{"with spaces", "  192.168.1.1  ,  10.0.0.1  ", "192.168.1.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := createTestContext("GET", "/", map[string]string{"X-Forwarded-For": tt.xff})
			ip := extractIP(ctx, trusted)
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
	trusted := parseTrustedProxies([]string{"192.0.2.1"})
	ctx, _ := createTestContext("GET", "/", map[string]string{"X-Real-IP": "10.0.0.1"})
	ip := extractIP(ctx, trusted)
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
	trusted := parseTrustedProxies([]string{"192.0.2.1"})
	ctx, _ := createTestContext("GET", "/", map[string]string{
		"X-Forwarded-For": "192.168.1.1",
		"X-Real-IP":       "10.0.0.1",
	})
	ip := extractIP(ctx, trusted)
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
				ctx := &Context{Request: req, Response: rec, params: make(map[string]string), values: make(map[string]interface{})}
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
		params:   make(map[string]string),
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
