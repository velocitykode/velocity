package csrf

import (
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/velocitykode/velocity/pkg/router"
)

// Global CSRF instance for template helpers
var globalCSRF *CSRF

// SetGlobalCSRF sets the global CSRF instance for template helpers
func SetGlobalCSRF(csrf *CSRF) {
	globalCSRF = csrf
}

// CSRFField returns an HTML hidden input field with the CSRF token
func CSRFField(sessionID string) template.HTML {
	if globalCSRF == nil {
		return template.HTML("")
	}

	token, err := globalCSRF.GetToken(sessionID)
	if err != nil {
		return template.HTML("")
	}

	return template.HTML(fmt.Sprintf(`<input type="hidden" name="_token" value="%s">`, template.HTMLEscapeString(token)))
}

// CSRFMeta returns an HTML meta tag with the CSRF token
func CSRFMeta(sessionID string) template.HTML {
	if globalCSRF == nil {
		return template.HTML("")
	}

	token, err := globalCSRF.GetToken(sessionID)
	if err != nil {
		return template.HTML("")
	}

	return template.HTML(fmt.Sprintf(`<meta name="csrf-token" content="%s">`, template.HTMLEscapeString(token)))
}

// CSRFToken returns the raw CSRF token value
func CSRFToken(sessionID string) string {
	if globalCSRF == nil {
		return ""
	}

	token, err := globalCSRF.GetToken(sessionID)
	if err != nil {
		return ""
	}

	return token
}

// GetGlobalToken returns the CSRF token for a session ID with error handling
func GetGlobalToken(sessionID string) (string, error) {
	if globalCSRF == nil {
		return "", fmt.Errorf("global CSRF instance not initialized")
	}

	return globalCSRF.GetToken(sessionID)
}

// Middleware returns the global CSRF middleware
func Middleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if globalCSRF == nil {
				return next(c)
			}
			// Track whether the inner handler was called (CSRF passed)
			var called bool
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				router.Wrap(next).ServeHTTP(w, r)
			})
			globalCSRF.Middleware(inner).ServeHTTP(c.Response, c.Request)
			if !called {
				log.Printf("csrf: request blocked for %s %s", c.Request.Method, c.Request.URL.Path)
			}
			return nil
		}
	}
}
