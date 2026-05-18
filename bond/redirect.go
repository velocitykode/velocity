package bond

import (
	"net/http"
	"net/url"
	"strings"
)

// Redirect performs an SPA-compatible redirect
// Uses 303 See Other for POST-Redirect-GET pattern
func (b *Bond) Redirect(w http.ResponseWriter, r *http.Request, url string) {
	b.RedirectWithStatus(w, r, url, http.StatusSeeOther)
}

// RedirectWithStatus performs a redirect with a custom status code.
// The URL is validated to prevent open redirects: only relative paths and
// same-host URLs are allowed. Use Location() for external redirects.
func (b *Bond) RedirectWithStatus(w http.ResponseWriter, r *http.Request, rawURL string, status int) {
	rawURL = sanitizeRedirectURL(rawURL, r.Host)
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
		referer = sanitizeRedirectURL(referer, r.Host)
	}
	b.Redirect(w, r, referer)
}

// sanitizeRedirectURL validates a redirect URL to prevent open redirects.
// Returns "/" if the URL is absolute and points to a different host, uses
// a dangerous scheme (javascript:, data:, vbscript:, file:, etc.), or fails
// to parse.
func sanitizeRedirectURL(target, host string) string {
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

	// Reject cross-host absolute URLs.
	if u.Host != "" && u.Host != host {
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
