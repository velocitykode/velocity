package router

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitConfig holds configuration for rate limiting middleware.
type RateLimitConfig struct {
	Burst           int
	Skip            func(*Context) bool
	OnLimitReached  func(*Context)
	Message         string
	CleanupInterval time.Duration
}

// RateLimitOption is a functional option for configuring rate limiters.
type RateLimitOption func(*RateLimitConfig)

// WithBurst sets the burst size (number of requests allowed above the limit).
func WithBurst(burst int) RateLimitOption {
	return func(cfg *RateLimitConfig) {
		cfg.Burst = burst
	}
}

// WithSkip sets a function to determine if a request should skip rate limiting.
func WithSkip(skip func(*Context) bool) RateLimitOption {
	return func(cfg *RateLimitConfig) {
		cfg.Skip = skip
	}
}

// WithOnLimitReached sets a callback invoked when the rate limit is exceeded.
func WithOnLimitReached(callback func(*Context)) RateLimitOption {
	return func(cfg *RateLimitConfig) {
		cfg.OnLimitReached = callback
	}
}

// WithMessage sets a custom error message for rate limit responses.
func WithMessage(message string) RateLimitOption {
	return func(cfg *RateLimitConfig) {
		cfg.Message = message
	}
}

// WithCleanupInterval sets the interval for cleaning up stale limiters.
func WithCleanupInterval(interval time.Duration) RateLimitOption {
	return func(cfg *RateLimitConfig) {
		cfg.CleanupInterval = interval
	}
}

// limiterEntry holds a rate limiter and its last access time.
type limiterEntry struct {
	limiter    *rate.Limiter
	lastAccess atomic.Int64 // Unix nano timestamp for atomic access
}

// keyedRateLimiter manages per-key rate limiters with automatic cleanup.
type keyedRateLimiter struct {
	mu       sync.RWMutex
	limiters map[string]*limiterEntry
	rate     rate.Limit
	burst    int
	window   time.Duration
	stopCh   chan struct{}
	stopped  bool
}

// newKeyedRateLimiter creates a new keyed rate limiter with cleanup.
func newKeyedRateLimiter(requests int, window time.Duration, burst int, cleanupInterval time.Duration) *keyedRateLimiter {
	krl := &keyedRateLimiter{
		limiters: make(map[string]*limiterEntry),
		rate:     rate.Limit(float64(requests) / window.Seconds()),
		burst:    burst,
		window:   window,
		stopCh:   make(chan struct{}),
	}

	// Start cleanup goroutine
	go krl.cleanup(cleanupInterval)

	return krl
}

// getLimiter returns the rate limiter for the given key, creating one if needed.
func (krl *keyedRateLimiter) getLimiter(key string) *rate.Limiter {
	krl.mu.RLock()
	entry, exists := krl.limiters[key]
	if exists {
		entry.lastAccess.Store(time.Now().UnixNano())
		krl.mu.RUnlock()
		return entry.limiter
	}
	krl.mu.RUnlock()

	krl.mu.Lock()
	defer krl.mu.Unlock()

	// Double-check after acquiring write lock
	if entry, exists := krl.limiters[key]; exists {
		entry.lastAccess.Store(time.Now().UnixNano())
		return entry.limiter
	}

	entry = &limiterEntry{
		limiter: rate.NewLimiter(krl.rate, krl.burst),
	}
	entry.lastAccess.Store(time.Now().UnixNano())
	krl.limiters[key] = entry
	return entry.limiter
}

// cleanup removes stale limiters periodically.
func (krl *keyedRateLimiter) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			krl.mu.Lock()
			now := time.Now().UnixNano()
			threshold := krl.window.Nanoseconds() * 2
			for key, entry := range krl.limiters {
				// Remove limiters that haven't been accessed for 2x the window
				if now-entry.lastAccess.Load() > threshold {
					delete(krl.limiters, key)
				}
			}
			krl.mu.Unlock()
		case <-krl.stopCh:
			return
		}
	}
}

// stop stops the cleanup goroutine.
func (krl *keyedRateLimiter) stop() {
	krl.mu.Lock()
	defer krl.mu.Unlock()
	if !krl.stopped {
		close(krl.stopCh)
		krl.stopped = true
	}
}

// count returns the number of active limiters.
func (krl *keyedRateLimiter) count() int {
	krl.mu.RLock()
	defer krl.mu.RUnlock()
	return len(krl.limiters)
}

// rateLimitResponse sends a rate limit exceeded response.
func rateLimitResponse(c *Context, limit int, remaining int, resetTime time.Time, message string) error {
	retryAfter := int(time.Until(resetTime).Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}

	c.SetHeader("Retry-After", strconv.Itoa(retryAfter))
	c.SetHeader("X-RateLimit-Limit", strconv.Itoa(limit))
	c.SetHeader("X-RateLimit-Remaining", strconv.Itoa(remaining))
	c.SetHeader("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))

	return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
		"code":    http.StatusTooManyRequests,
		"message": message,
	})
}

// setRateLimitHeaders sets the rate limit headers on a successful request.
func setRateLimitHeaders(c *Context, limit int, remaining int, resetTime time.Time) {
	c.SetHeader("X-RateLimit-Limit", strconv.Itoa(limit))
	c.SetHeader("X-RateLimit-Remaining", strconv.Itoa(remaining))
	c.SetHeader("X-RateLimit-Reset", strconv.FormatInt(resetTime.Unix(), 10))
}

// RateLimit creates a global rate limiter middleware.
// It limits all requests to the specified number within the given time window.
func RateLimit(requests int, window time.Duration, opts ...RateLimitOption) MiddlewareFunc {
	cfg := &RateLimitConfig{
		Burst:           requests, // Default burst equals requests
		Message:         "Rate limit exceeded",
		CleanupInterval: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	limiter := rate.NewLimiter(rate.Limit(float64(requests)/window.Seconds()), cfg.Burst)
	windowStart := time.Now()
	var mu sync.Mutex

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Check skip function
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}

			mu.Lock()
			now := time.Now()
			// Reset window if expired
			if now.Sub(windowStart) >= window {
				windowStart = now
			}
			resetTime := windowStart.Add(window)

			allowed := limiter.Allow()
			// Calculate remaining tokens (approximate)
			tokens := int(limiter.Tokens())
			if tokens < 0 {
				tokens = 0
			}
			mu.Unlock()

			if !allowed {
				if cfg.OnLimitReached != nil {
					cfg.OnLimitReached(c)
				}
				return rateLimitResponse(c, requests, 0, resetTime, cfg.Message)
			}

			setRateLimitHeaders(c, requests, tokens, resetTime)
			return next(c)
		}
	}
}

// RateLimitByKey creates a per-key rate limiter middleware.
// Each unique key (returned by keyFunc) has its own rate limit.
func RateLimitByKey(requests int, window time.Duration, keyFunc func(*Context) string, opts ...RateLimitOption) MiddlewareFunc {
	cfg := &RateLimitConfig{
		Burst:           requests,
		Message:         "Rate limit exceeded",
		CleanupInterval: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	krl := newKeyedRateLimiter(requests, window, cfg.Burst, cfg.CleanupInterval)

	// Track window starts per key
	windowStarts := &sync.Map{}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Check skip function
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}

			key := keyFunc(c)
			limiter := krl.getLimiter(key)

			now := time.Now()
			// Get or create window start for this key
			wsInterface, _ := windowStarts.LoadOrStore(key, now)
			ws := wsInterface.(time.Time)
			if now.Sub(ws) >= window {
				windowStarts.Store(key, now)
				ws = now
			}
			resetTime := ws.Add(window)

			allowed := limiter.Allow()
			tokens := int(limiter.Tokens())
			if tokens < 0 {
				tokens = 0
			}

			if !allowed {
				if cfg.OnLimitReached != nil {
					cfg.OnLimitReached(c)
				}
				return rateLimitResponse(c, requests, 0, resetTime, cfg.Message)
			}

			setRateLimitHeaders(c, requests, tokens, resetTime)
			return next(c)
		}
	}
}

// RateLimitByIP creates a per-IP rate limiter middleware.
// It extracts the client IP from X-Forwarded-For, X-Real-IP, or RemoteAddr.
func RateLimitByIP(requests int, window time.Duration, opts ...RateLimitOption) MiddlewareFunc {
	return RateLimitByKey(requests, window, func(c *Context) string {
		return extractIP(c)
	}, opts...)
}

// extractIP extracts the client IP address from the request.
// It checks X-Forwarded-For, X-Real-IP, then falls back to RemoteAddr.
func extractIP(c *Context) string {
	// Check X-Forwarded-For header (may contain comma-separated list)
	if xff := c.Header("X-Forwarded-For"); xff != "" {
		// Take the first IP in the list (original client)
		if idx := strings.Index(xff, ","); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}

	// Check X-Real-IP header
	if xri := c.Header("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// Fall back to RemoteAddr
	addr := c.Request.RemoteAddr
	// Strip port if present
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		// Check if this looks like IPv6 with port [::1]:8080
		if strings.Contains(addr, "]") {
			if bracketIdx := strings.LastIndex(addr, "]"); bracketIdx != -1 && idx > bracketIdx {
				return addr[:idx]
			}
			// IPv6 in brackets without port, return as-is
			return addr
		}
		// Check if this is a bare IPv6 address (contains multiple colons)
		if strings.Count(addr, ":") > 1 {
			// This is an IPv6 address without brackets, return as-is
			return addr
		}
		// IPv4 with port
		return addr[:idx]
	}
	return addr
}

// RateLimitStore is an interface for custom rate limit storage backends.
type RateLimitStore interface {
	// Allow checks if the request is allowed and records it.
	// Returns true if allowed, along with remaining requests and reset time.
	Allow(key string) (allowed bool, remaining int, resetTime time.Time)
}

// RateLimitWithStore creates a rate limiter middleware using a custom store.
func RateLimitWithStore(store RateLimitStore, keyFunc func(*Context) string, opts ...RateLimitOption) MiddlewareFunc {
	cfg := &RateLimitConfig{
		Message: "Rate limit exceeded",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}

			key := keyFunc(c)
			allowed, remaining, resetTime := store.Allow(key)

			if !allowed {
				if cfg.OnLimitReached != nil {
					cfg.OnLimitReached(c)
				}
				return rateLimitResponse(c, remaining, 0, resetTime, cfg.Message)
			}

			setRateLimitHeaders(c, remaining, remaining, resetTime)
			return next(c)
		}
	}
}

// rateLimiterWithCleanup is a helper for testing that exposes cleanup control.
type rateLimiterWithCleanup struct {
	krl *keyedRateLimiter
	mw  MiddlewareFunc
}

// RateLimitByKeyWithCleanupControl creates a per-key rate limiter with cleanup control for testing.
func RateLimitByKeyWithCleanupControl(requests int, window time.Duration, keyFunc func(*Context) string, opts ...RateLimitOption) (*rateLimiterWithCleanup, MiddlewareFunc) {
	cfg := &RateLimitConfig{
		Burst:           requests,
		Message:         "Rate limit exceeded",
		CleanupInterval: 5 * time.Minute,
	}
	for _, opt := range opts {
		opt(cfg)
	}

	krl := newKeyedRateLimiter(requests, window, cfg.Burst, cfg.CleanupInterval)
	windowStarts := &sync.Map{}

	mw := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if cfg.Skip != nil && cfg.Skip(c) {
				return next(c)
			}

			key := keyFunc(c)
			limiter := krl.getLimiter(key)

			now := time.Now()
			wsInterface, _ := windowStarts.LoadOrStore(key, now)
			ws := wsInterface.(time.Time)
			if now.Sub(ws) >= window {
				windowStarts.Store(key, now)
				ws = now
			}
			resetTime := ws.Add(window)

			allowed := limiter.Allow()
			tokens := int(limiter.Tokens())
			if tokens < 0 {
				tokens = 0
			}

			if !allowed {
				if cfg.OnLimitReached != nil {
					cfg.OnLimitReached(c)
				}
				return rateLimitResponse(c, requests, 0, resetTime, cfg.Message)
			}

			setRateLimitHeaders(c, requests, tokens, resetTime)
			return next(c)
		}
	}

	return &rateLimiterWithCleanup{krl: krl, mw: mw}, mw
}

// Stop stops the cleanup goroutine.
func (r *rateLimiterWithCleanup) Stop() {
	r.krl.stop()
}

// Count returns the number of active limiters.
func (r *rateLimiterWithCleanup) Count() int {
	return r.krl.count()
}

// ForceCleanup forces an immediate cleanup for testing.
func (r *rateLimiterWithCleanup) ForceCleanup() {
	r.krl.mu.Lock()
	defer r.krl.mu.Unlock()
	now := time.Now().UnixNano()
	threshold := r.krl.window.Nanoseconds() * 2
	for key, entry := range r.krl.limiters {
		if now-entry.lastAccess.Load() > threshold {
			delete(r.krl.limiters, key)
		}
	}
}

// SetLastAccess sets the last access time for a key (for testing).
func (r *rateLimiterWithCleanup) SetLastAccess(key string, t time.Time) {
	r.krl.mu.Lock()
	defer r.krl.mu.Unlock()
	if entry, exists := r.krl.limiters[key]; exists {
		entry.lastAccess.Store(t.UnixNano())
	}
}

// Throttle is an alias for RateLimitByIP for convenience.
func Throttle(requests int, window time.Duration, opts ...RateLimitOption) MiddlewareFunc {
	return RateLimitByIP(requests, window, opts...)
}

// ThrottleByKey is an alias for RateLimitByKey for convenience.
func ThrottleByKey(requests int, window time.Duration, keyFunc func(*Context) string, opts ...RateLimitOption) MiddlewareFunc {
	return RateLimitByKey(requests, window, keyFunc, opts...)
}

// NewRateLimitGroup creates a group of rate limiters that all must pass.
// Useful for having multiple limits (e.g., 10/second AND 100/minute).
func NewRateLimitGroup(limiters ...MiddlewareFunc) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		// Build the chain
		handler := next
		for i := len(limiters) - 1; i >= 0; i-- {
			handler = limiters[i](handler)
		}
		return handler
	}
}

// Example usage in doc comments:

// Example_basicRateLimit demonstrates basic global rate limiting.
//
//	router.Use(RateLimit(100, time.Minute))
//
// Example_rateLimitByIP demonstrates per-IP rate limiting.
//
//	router.Use(RateLimitByIP(10, time.Second))
//
// Example_rateLimitByKey demonstrates per-key rate limiting.
//
//	router.Use(RateLimitByKey(100, time.Hour, func(c *Context) string {
//	    return c.Header("X-API-Key")
//	}))
//
// Example_rateLimitWithOptions demonstrates rate limiting with options.
//
//	router.Use(RateLimitByIP(100, time.Minute,
//	    WithBurst(10),
//	    WithSkip(func(c *Context) bool {
//	        return c.Path() == "/health"
//	    }),
//	    WithOnLimitReached(func(c *Context) {
//	        log.Printf("Rate limit exceeded for %s", c.IP())
//	    }),
//	    WithMessage("Too many requests, please slow down"),
//	    WithCleanupInterval(5*time.Minute),
//	))
//
// Example_multipleRateLimits demonstrates combining multiple rate limits.
//
//	router.Use(NewRateLimitGroup(
//	    RateLimitByIP(10, time.Second),   // 10 per second
//	    RateLimitByIP(100, time.Minute),  // 100 per minute
//	))

// String returns a description of the rate limit configuration.
func (cfg *RateLimitConfig) String() string {
	return fmt.Sprintf("RateLimitConfig{Burst: %d, Message: %q, CleanupInterval: %v}",
		cfg.Burst, cfg.Message, cfg.CleanupInterval)
}
