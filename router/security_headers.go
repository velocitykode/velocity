package router

import (
	"fmt"
	"net"
	"net/http"
)

// SecurityHeadersConfig holds configuration for the SecurityHeaders middleware.
type SecurityHeadersConfig struct {
	CSP                   string
	HSTSEnabled           bool
	HSTSMaxAge            int
	HSTSIncludeSubDomains bool
	FrameOptions          string
	ReferrerPolicy        string
	PermissionsPolicy     string
	CrossDomainPolicies   string
	ContentTypeNoSniff    bool
	XSSProtection         string
}

// SecurityHeadersOption is a functional option for configuring SecurityHeaders.
type SecurityHeadersOption func(*SecurityHeadersConfig)

// WithCSP sets the Content-Security-Policy header value.
func WithCSP(policy string) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.CSP = policy
	}
}

// WithHSTS enables or disables the Strict-Transport-Security header.
func WithHSTS(enabled bool) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.HSTSEnabled = enabled
	}
}

// WithHSTSMaxAge sets the max-age directive for Strict-Transport-Security.
func WithHSTSMaxAge(seconds int) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.HSTSMaxAge = seconds
	}
}

// WithFrameOptions sets the X-Frame-Options header value.
func WithFrameOptions(value string) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.FrameOptions = value
	}
}

// WithReferrerPolicy sets the Referrer-Policy header value.
func WithReferrerPolicy(policy string) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.ReferrerPolicy = policy
	}
}

// WithPermissionsPolicy sets the Permissions-Policy header value.
func WithPermissionsPolicy(policy string) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.PermissionsPolicy = policy
	}
}

// WithCrossDomainPolicies sets the X-Permitted-Cross-Domain-Policies header value.
func WithCrossDomainPolicies(value string) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.CrossDomainPolicies = value
	}
}

// SecurityHeaders returns a middleware that sets common security response headers.
// These headers help protect against common web vulnerabilities like MIME sniffing,
// clickjacking, and information leakage. All headers are configurable via options.
func SecurityHeaders(opts ...SecurityHeadersOption) MiddlewareFunc {
	cfg := &SecurityHeadersConfig{
		CSP:                   "default-src 'self'",
		HSTSEnabled:           true,
		HSTSMaxAge:            63072000,
		HSTSIncludeSubDomains: true,
		FrameOptions:          "DENY",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "camera=(), microphone=(), geolocation=()",
		CrossDomainPolicies:   "none",
		ContentTypeNoSniff:    true,
		XSSProtection:         "0",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	// Pre-compute HSTS value to avoid per-request formatting
	var hstsValue string
	if cfg.HSTSEnabled {
		hstsValue = fmt.Sprintf("max-age=%d", cfg.HSTSMaxAge)
		if cfg.HSTSIncludeSubDomains {
			hstsValue += "; includeSubDomains"
		}
	}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if cfg.ContentTypeNoSniff {
				c.SetHeader("X-Content-Type-Options", "nosniff")
			}
			if cfg.FrameOptions != "" {
				c.SetHeader("X-Frame-Options", cfg.FrameOptions)
			}
			if cfg.ReferrerPolicy != "" {
				c.SetHeader("Referrer-Policy", cfg.ReferrerPolicy)
			}
			c.SetHeader("X-XSS-Protection", cfg.XSSProtection)
			if cfg.PermissionsPolicy != "" {
				c.SetHeader("Permissions-Policy", cfg.PermissionsPolicy)
			}
			if cfg.CSP != "" {
				c.SetHeader("Content-Security-Policy", cfg.CSP)
			}
			if cfg.HSTSEnabled {
				c.SetHeader("Strict-Transport-Security", hstsValue)
			}
			if cfg.CrossDomainPolicies != "" {
				c.SetHeader("X-Permitted-Cross-Domain-Policies", cfg.CrossDomainPolicies)
			}
			return next(c)
		}
	}
}

// httpsRedirectConfig holds configuration for the HTTPSRedirect middleware.
type httpsRedirectConfig struct {
	trustedProxies []*net.IPNet
	excludePaths   map[string]bool
}

// HTTPSRedirectOption is a functional option for configuring HTTPSRedirect.
type HTTPSRedirectOption func(*httpsRedirectConfig)

// WithHTTPSRedirectTrustedProxies sets trusted proxy IPs/CIDRs for the HTTPS redirect middleware.
// When configured, X-Forwarded-Proto headers are trusted only from these proxies.
func WithHTTPSRedirectTrustedProxies(proxies []string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		cfg.trustedProxies = parseTrustedProxies(proxies)
	}
}

// WithExcludePaths sets paths that should be excluded from HTTPS redirects.
// Useful for health check endpoints that need to remain accessible over HTTP.
func WithExcludePaths(paths ...string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		for _, p := range paths {
			cfg.excludePaths[p] = true
		}
	}
}

// HTTPSRedirect returns a middleware that redirects HTTP requests to HTTPS.
// It checks X-Forwarded-Proto only when trusted proxies are configured and the
// direct connection comes from a trusted proxy. Excluded paths skip the redirect.
func HTTPSRedirect(opts ...HTTPSRedirectOption) MiddlewareFunc {
	cfg := &httpsRedirectConfig{
		excludePaths: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Skip excluded paths
			if cfg.excludePaths[c.Request.URL.Path] {
				return next(c)
			}

			// Already HTTPS via direct TLS
			if c.Request.TLS != nil {
				return next(c)
			}

			// Check X-Forwarded-Proto from trusted proxies
			if len(cfg.trustedProxies) > 0 {
				remoteIP := stripPort(c.Request.RemoteAddr)
				if isTrustedProxy(remoteIP, cfg.trustedProxies) {
					if c.Request.Header.Get("X-Forwarded-Proto") == "https" {
						return next(c)
					}
				}
			}

			// Redirect to HTTPS
			httpsURL := "https://" + c.Request.Host + c.Request.RequestURI
			c.SetHeader("Location", httpsURL)
			c.Response.WriteHeader(http.StatusMovedPermanently)
			return nil
		}
	}
}

