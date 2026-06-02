package router

import (
	"fmt"
	"net/http"
	"strings"
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

// WithHSTSIncludeSubDomains controls whether includeSubDomains is added to the HSTS header.
func WithHSTSIncludeSubDomains(include bool) SecurityHeadersOption {
	return func(cfg *SecurityHeadersConfig) {
		cfg.HSTSIncludeSubDomains = include
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
	trustedProxies *TrustedProxies
	// trustedProxyErr is carried from WithHTTPSRedirectTrustedProxies so
	// HTTPSRedirect can fail-fast at construction rather than silently
	// ignoring a malformed CIDR.
	trustedProxyErr  error
	excludePaths     map[string]bool
	allowedHosts     map[string]bool
	canonicalHost    string
	firstAllowedHost string
}

// HTTPSRedirectOption is a functional option for configuring HTTPSRedirect.
type HTTPSRedirectOption func(*httpsRedirectConfig)

// WithHTTPSRedirectTrustedProxies sets trusted proxy IPs/CIDRs for the HTTPS redirect middleware.
// When configured, X-Forwarded-Proto headers are trusted only from these proxies.
//
// Invalid entries are retained on the config and surfaced when
// HTTPSRedirect is constructed — which panics, matching the existing
// contract for middleware-level configuration errors.
func WithHTTPSRedirectTrustedProxies(proxies []string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		tp, err := ParseTrustedProxies(proxies)
		if err != nil {
			cfg.trustedProxyErr = err
			return
		}
		cfg.trustedProxies = tp
	}
}

// WithExcludePaths sets paths that should be excluded from HTTPS redirects.
// Useful for health check endpoints that need to remain accessible over HTTP.
func WithExcludePaths(paths ...string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		if cfg.excludePaths == nil {
			cfg.excludePaths = make(map[string]bool)
		}
		for _, p := range paths {
			cfg.excludePaths[p] = true
		}
	}
}

// WithHTTPSRedirectAllowedHosts sets hosts that may be reflected into HTTPS
// redirect Location headers.
func WithHTTPSRedirectAllowedHosts(hosts ...string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		if cfg.allowedHosts == nil {
			cfg.allowedHosts = make(map[string]bool)
		}
		for _, host := range hosts {
			if cfg.firstAllowedHost == "" {
				cfg.firstAllowedHost = host
			}
			cfg.allowedHosts[host] = true
		}
	}
}

// WithHTTPSRedirectCanonicalHost sets the host to use when the request Host is
// not allow-listed.
func WithHTTPSRedirectCanonicalHost(host string) HTTPSRedirectOption {
	return func(cfg *httpsRedirectConfig) {
		cfg.canonicalHost = host
	}
}

// HTTPSRedirect returns a middleware that redirects HTTP requests to HTTPS.
// It checks X-Forwarded-Proto only when trusted proxies are configured and the
// direct connection comes from a trusted proxy. Excluded paths skip the redirect.
// Operators SHOULD configure WithHTTPSRedirectAllowedHosts, or enforce a host
// allowlist at the edge, to fully close Host-header injection exposure in
// redirect Location headers.
//
// Panics with a wrapped ErrInvalidTrustedProxy if any
// WithHTTPSRedirectTrustedProxies entry was malformed — boot-time
// misconfiguration should not ship.
// httpsRedirectHost decides which Host to reflect into the HTTPS redirect
// Location. It validates the requested Host against BOTH the middleware-local
// allowlist (WithHTTPSRedirectAllowedHosts) and the router-level
// Router.RedirectAllowedHosts, so an app that configured the router allowlist
// is protected here too without repeating it, and the two cannot diverge.
// When an allowlist is configured and the requested Host is not on it, the
// configured canonical host (or the first allowlisted host) is reflected
// instead of the raw, attacker-controlled Host. With no allowlist configured
// anywhere, the requested Host is reflected unchanged (documented residual:
// configure an allowlist or a canonical host to close it).
func httpsRedirectHost(requested string, cfg *httpsRedirectConfig, routerHosts []string) string {
	reqLower := strings.ToLower(requested)
	allowed := false
	listPresent := false
	for h := range cfg.allowedHosts {
		listPresent = true
		if strings.ToLower(h) == reqLower {
			allowed = true
			break
		}
	}
	if !allowed {
		for _, h := range routerHosts {
			listPresent = true
			if strings.ToLower(h) == reqLower {
				allowed = true
				break
			}
		}
	}
	if !listPresent || allowed {
		return requested
	}
	// Not allowlisted: fall back to a configured-safe host rather than
	// reflecting the raw Host.
	if cfg.canonicalHost != "" {
		return cfg.canonicalHost
	}
	if cfg.firstAllowedHost != "" {
		return cfg.firstAllowedHost
	}
	if len(routerHosts) > 0 {
		return routerHosts[0]
	}
	return requested
}

func HTTPSRedirect(opts ...HTTPSRedirectOption) MiddlewareFunc {
	cfg := &httpsRedirectConfig{
		excludePaths: make(map[string]bool),
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.trustedProxyErr != nil {
		panic(fmt.Errorf("velocity/router: https redirect: %w", cfg.trustedProxyErr))
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

			// Check X-Forwarded-Proto from trusted proxies.
			// Intentionally uses raw RemoteAddr (not clientip.Extract):
			// we are asking whether the immediate peer is a trusted
			// proxy, not who the original client is.
			if cfg.trustedProxies != nil && cfg.trustedProxies.Len() > 0 {
				remoteIP := stripPortHost(c.Request.RemoteAddr)
				if cfg.trustedProxies.Contains(remoteIP) {
					if c.Request.Header.Get("X-Forwarded-Proto") == "https" {
						return next(c)
					}
				}
			}

			// Redirect to HTTPS. Validate the Host against the configured
			// allowlists before reflecting it so an attacker-controlled Host
			// cannot be echoed into the Location header (cache-poisoning /
			// host-header injection, F34).
			host := httpsRedirectHost(c.Request.Host, cfg, c.redirectAllowedHosts)
			httpsURL := "https://" + host + c.Request.RequestURI
			c.SetHeader("Vary", "Host")
			c.SetHeader("Location", httpsURL)
			c.Response.WriteHeader(http.StatusMovedPermanently)
			return nil
		}
	}
}
