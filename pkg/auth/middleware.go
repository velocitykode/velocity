package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/velocitykode/velocity/pkg/router"
)

// wantsJSON returns true if the request expects a JSON response.
func wantsJSON(r *http.Request) bool {
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
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
