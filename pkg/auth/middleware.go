package auth

import (
	"net/http"
	"net/url"
)

// authChecker is an internal variable that can be overridden for testing
var authChecker = Check

// Guest middleware - redirects authenticated users
func Guest(redirectTo string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authChecker(r) {
				http.Redirect(w, r, redirectTo, http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RedirectIfAuthenticated middleware - same as Guest but with clearer name
func RedirectIfAuthenticated(redirectTo string) func(http.Handler) http.Handler {
	return Guest(redirectTo)
}

// Middleware that requires authentication
func Middleware(redirectTo string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authChecker(r) {
				// Store intended URL for redirect after login
				redirectURL := r.URL.Path
				if r.URL.RawQuery != "" {
					redirectURL += "?" + r.URL.RawQuery
				}
				// URL encode the redirect parameter
				escapedURL := url.QueryEscape(redirectURL)
				http.Redirect(w, r, redirectTo+"?redirect="+escapedURL, http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth is an alias for Middleware
func RequireAuth(redirectTo string) func(http.Handler) http.Handler {
	return Middleware(redirectTo)
}
