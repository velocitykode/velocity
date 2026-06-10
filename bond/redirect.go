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
	// stripCRLF before any Header().Set defends in depth against header
	// injection. Go's net/http catches CR/LF in header values at write
	// time, but relying on that is fragile (the panic surfaces only when
	// the response is committed, and middleware that inspects the header
	// in the meantime sees the raw value). Sanitise at the source.
	rawURL = stripCRLF(rawURL)
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
	// Defence-in-depth: strip CR/LF before any Header().Set even though
	// net/http will reject them at write time. Same rationale as
	// RedirectWithStatus above; see stripCRLF.
	url = stripCRLF(url)
	if isInertiaRequest(r) {
		// 409 Conflict with X-Inertia-Location triggers full page reload
		w.Header().Set("X-Inertia-Location", url)
		w.WriteHeader(http.StatusConflict)
		return
	}
	// For non-Inertia requests, use standard redirect
	http.Redirect(w, r, url, http.StatusFound)
}

// stripCRLF removes ASCII CR and LF bytes from a header value. Returns
// the original string when no CR/LF is present (avoids the allocation
// on the happy path). This is a defence-in-depth helper applied at
// every Header().Set sink that writes a URL coming from a non-trivial
// source (the redirect path, the buffered Location rewrite in
// Middleware, etc.). It mirrors the CRLF reject performed by mail's
// Address.Validate and router's Context.SetHeader, so bond cannot
// regress to a strictly-weaker stance than the rest of the framework.
func stripCRLF(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	r := strings.NewReplacer("\r", "", "\n", "")
	return r.Replace(s)
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
// It delegates the shared checks (empty input, slash lookalikes,
// protocol-relative references, scheme-without-host, host allowlist) to
// the canonical router.SanitizeRedirect, then layers two bond-specific
// STRICTER rules on absolute URLs:
//
//   - only http/https schemes are accepted even for allowlisted hosts
//     (the router accepts any scheme once the host passes the allowlist);
//   - a parsed path beginning with "//" is rejected, because some
//     intermediaries normalise it back into a network-path reference.
//
// Returns "/" for anything rejected. An empty allowedHosts list rejects
// every absolute URL (relative paths still flow through).
func sanitizeRedirectURL(target string, allowedHosts []string) string {
	// router.SanitizeRedirect returns target verbatim when accepted and
	// "/" when rejected, so any change means rejection ("/" itself is
	// accepted unchanged).
	if router.SanitizeRedirect(target, allowedHosts) != target {
		return "/"
	}

	// Relative paths need none of the absolute-URL extras.
	if strings.HasPrefix(target, "/") {
		return target
	}

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

	// Some inputs parse with an empty host but a path that starts with "//".
	// Browsers and some intermediaries may normalise these back into a
	// network-path reference, so reject them.
	if strings.HasPrefix(u.Path, "//") {
		return "/"
	}

	return target
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
