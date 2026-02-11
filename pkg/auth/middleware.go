package auth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/velocitykode/velocity/pkg/router"
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

// wantsJSON returns true if the request expects a JSON response.
func wantsJSON(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
}

// Middleware that requires authentication
func Middleware(redirectTo string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authChecker(r) {
				if wantsJSON(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					json.NewEncoder(w).Encode(map[string]string{"error": "Unauthenticated."})
					return
				}
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

// AuthMiddleware returns a router.MiddlewareFunc that requires authentication
// using the provided Manager instance.
func AuthMiddleware(manager *Manager) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if !manager.Check(c.Request) {
				if wantsJSON(c.Request) {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Unauthenticated."})
				}
				redirectURL := c.Request.URL.Path
				if c.Request.URL.RawQuery != "" {
					redirectURL += "?" + c.Request.URL.RawQuery
				}
				escapedURL := url.QueryEscape(redirectURL)
				return c.Redirect(http.StatusSeeOther, "/login?redirect="+escapedURL)
			}
			return next(c)
		}
	}
}
