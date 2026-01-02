package bond

import "net/http"

// Redirect performs an SPA-compatible redirect
// Uses 303 See Other for POST-Redirect-GET pattern
func (b *Bond) Redirect(w http.ResponseWriter, r *http.Request, url string) {
	b.RedirectWithStatus(w, r, url, http.StatusSeeOther)
}

// RedirectWithStatus performs a redirect with a custom status code
func (b *Bond) RedirectWithStatus(w http.ResponseWriter, r *http.Request, url string, status int) {
	if isInertiaRequest(r) {
		// For Inertia requests, set the location header for client-side handling
		w.Header().Set("X-Inertia-Location", url)
	}
	http.Redirect(w, r, url, status)
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

// Back redirects to the previous page using the Referer header
// Falls back to "/" if no Referer is present
func (b *Bond) Back(w http.ResponseWriter, r *http.Request) {
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	b.Redirect(w, r, referer)
}
