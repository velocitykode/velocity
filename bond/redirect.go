package bond

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/velocitykode/velocity/router"
)

// hostFallbackWarned is a process-wide latch that fires the
// "no RedirectAllowlist configured, falling back to r.Host" warning
// exactly once across all *Bond instances. The fallback is a security
// gap (r.Host is operator-spoofable via a misconfigured fronting proxy
// that forwards X-Forwarded-Host without sanitisation), so operators
// should see it surfaced, but repeating it on every redirect would
// flood logs without adding signal.
var hostFallbackWarned atomic.Bool

// Redirect performs an SPA-compatible redirect
// Uses 303 See Other for POST-Redirect-GET pattern
func (b *Bond) Redirect(w http.ResponseWriter, r *http.Request, url string) {
	b.RedirectWithStatus(w, r, url, http.StatusSeeOther)
}

// RedirectWithStatus performs a redirect with a custom status code.
// The URL is validated to prevent open redirects: only relative paths and
// same-host URLs are allowed. Use Location() for external redirects.
func (b *Bond) RedirectWithStatus(w http.ResponseWriter, r *http.Request, rawURL string, status int) {
	rawURL = sanitizeRedirectURL(rawURL, b.allowedHostsFor(r))
	if isInertiaRequest(r) {
		// For Inertia requests, set the location header for client-side handling
		w.Header().Set("X-Inertia-Location", rawURL)
	}
	http.Redirect(w, r, rawURL, status)
}

// Location forces a full page reload (external redirect)
// This breaks out of the SPA and performs a full navigation
// Use for external URLs or when you need to break out of Inertia
func (b *Bond) Location(w http.ResponseWriter, r *http.Request, url string) {
	if isInertiaRequest(r) {
		// 409 Conflict with X-Inertia-Location triggers full page reload
		w.Header().Set("X-Inertia-Location", url)
		w.WriteHeader(http.StatusConflict)
		return
	}
	// For non-Inertia requests, use standard redirect
	http.Redirect(w, r, url, http.StatusFound)
}

// Back redirects to the previous page using the Referer header.
// Only allows relative URLs or URLs matching the request host.
// Falls back to "/" if no Referer is present or if it points to an external domain.
func (b *Bond) Back(w http.ResponseWriter, r *http.Request) {
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	} else {
		referer = sanitizeRedirectURL(referer, b.allowedHostsFor(r))
	}
	b.Redirect(w, r, referer)
}

// allowedHostsFor returns the host allowlist bond will treat as
// same-origin for r. Order of resolution:
//
//  1. Services.RedirectAllowlist published by the framework. The router
//     implements this contract (it owns Router.RedirectAllowedHosts) so
//     a velocity.New()-wired deployment threads the operator-configured
//     list through here automatically.
//  2. When the contract is missing or empty, fall back to r.Host so
//     unit tests and stand-alone *Bond usage keep working. Emit a
//     process-wide one-time warning so operators see that no allowlist
//     is enforced. r.Host is operator-spoofable when X-Forwarded-Host
//     is forwarded blindly by a fronting proxy, and that is exactly the
//     scenario the allowlist exists to defeat.
//
// The returned slice is owned by the caller; sanitizeRedirectURL must
// not mutate it.
func (b *Bond) allowedHostsFor(r *http.Request) []string {
	if hosts := redirectAllowlistFromRequest(r); len(hosts) > 0 {
		return hosts
	}
	if hostFallbackWarned.CompareAndSwap(false, true) {
		b.mu.RLock()
		logger := b.logger
		b.mu.RUnlock()
		if logger != nil {
			logger.Warn(
				"velocity/bond: no RedirectAllowlist configured; falling back to r.Host for same-origin redirect checks. " +
					"A misconfigured fronting proxy that copies X-Forwarded-Host into r.Host can bypass open-redirect protection. " +
					"Set Router.RedirectAllowedHosts to your canonical hostnames.",
			)
		}
	}
	if r.Host == "" {
		return nil
	}
	return []string{r.Host}
}

// sanitizeRedirectURL validates a redirect URL to prevent open redirects.
// Returns "/" if the URL is absolute and points to a host outside
// allowedHosts, uses a dangerous scheme (javascript:, data:, vbscript:,
// file:, etc.), or fails to parse. An empty allowedHosts list rejects
// every absolute URL (relative paths still flow through).
func sanitizeRedirectURL(target string, allowedHosts []string) string {
	// Reject any protocol-relative or network-path target up front. This
	// covers "//evil.com", "///evil.com/path", "////evil.com", etc.
	// Browsers treat all of these as cross-origin.
	if strings.HasPrefix(target, "//") {
		return "/"
	}

	// Reject backslash variants like "/\evil.com" and "\\evil.com". Some
	// browsers and intermediaries normalise "\" to "/", which would turn
	// these into a network-path reference. Be conservative and reject any
	// target containing a backslash before we trust it as a relative path.
	if strings.ContainsRune(target, '\\') {
		return "/"
	}

	// Allow relative paths
	if strings.HasPrefix(target, "/") {
		return target
	}

	// Parse and validate absolute URLs
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}

	// Reject dangerous schemes (javascript:, data:, vbscript:, file:, etc.).
	// Only http/https are allowed for absolute URLs; an empty scheme is fine
	// because it indicates a relative URL.
	if u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https" {
		return "/"
	}

	// Reject cross-host absolute URLs. The host must appear in the
	// caller-supplied allowlist; we deliberately do not consult r.Host
	// here because a misconfigured fronting proxy could copy an
	// attacker-supplied X-Forwarded-Host into r.Host and bypass this
	// check. allowedHostsFor is the single source of truth for what
	// counts as "same-origin".
	if u.Host != "" && !hostInAllowlist(u.Host, allowedHosts) {
		return "/"
	}

	// Some inputs parse with an empty host but a path that starts with "//".
	// Browsers and some intermediaries may normalise these back into a
	// network-path reference, so reject them.
	if strings.HasPrefix(u.Path, "//") {
		return "/"
	}

	return target
}

// hostInAllowlist reports whether host appears in allowed. Exact match
// only; allowlist semantics intentionally do not include suffix or
// wildcard matching, matching the router's RedirectAllowedHosts contract.
func hostInAllowlist(host string, allowed []string) bool {
	for _, h := range allowed {
		if h != "" && host == h {
			return true
		}
	}
	return false
}

// redirectAllowlistFromRequest returns the operator-configured allowlist
// of cross-origin hosts that may be treated as "same-origin" for r, or
// nil when the request was not routed through velocity.New() (typical
// for unit tests that build a *Bond directly) or when no allowlist was
// configured.
//
// Callers that receive nil should decide their own fallback policy; see
// Bond.allowedHostsFor.
func redirectAllowlistFromRequest(r *http.Request) []string {
	services := router.ServicesFromRequest(r)
	if services == nil || services.RedirectAllowlist == nil {
		return nil
	}
	return services.RedirectAllowlist.AllowedRedirectHosts()
}
