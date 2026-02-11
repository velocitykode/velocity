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
// Returns "/" if the URL is absolute and points to a different host.
func sanitizeRedirectURL(target, host string) string {
	// Allow relative paths
	if strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "//") {
		return target
	}

	// Parse and validate absolute URLs
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}

	// Reject protocol-relative URLs (//evil.com)
	if u.Host != "" && u.Host != host {
		return "/"
	}

	return target
}
