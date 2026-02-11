package csrf

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/velocitykode/velocity/pkg/router"
)

// Global CSRF instance for template helpers
var globalCSRF *CSRF

func SetGlobalCSRF(csrf *CSRF) {
	globalCSRF = csrf
}

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

func GetGlobalToken(sessionID string) (string, error) {
	if globalCSRF == nil {
		return "", fmt.Errorf("global CSRF instance not initialized")
	}

	return globalCSRF.GetToken(sessionID)
}

func Middleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			if globalCSRF == nil {
				if strings.EqualFold(os.Getenv("APP_DEBUG"), "true") {
					log.Println("csrf: middleware invoked without initialization, all requests will pass through (APP_DEBUG=true)")
					return next(c)
				}
				log.Println("csrf: middleware invoked without initialization, blocking request")
				http.Error(c.Response, "CSRF protection not configured", http.StatusInternalServerError)
				return fmt.Errorf("csrf: protection not configured")
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
				return fmt.Errorf("csrf: request rejected for %s %s", c.Request.Method, c.Request.URL.Path)
			}
			return nil
		}
	}
}
