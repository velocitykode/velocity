package router

import (
	"net/http"
)

// csrfVerifier is the interface that csrf_middleware expects.
// It is satisfied by *csrf.CSRF without importing that package, which
// avoids an import cycle (csrf already imports router).
type csrfVerifier interface {
	VerifyToken(r *http.Request) error
	Token(r *http.Request) string
}

// CSRFMiddleware returns a middleware that verifies CSRF tokens on
// state-changing requests (anything other than GET, HEAD, OPTIONS).
// The csrfInstance must implement VerifyToken and Token; if it does not,
// all requests pass through unmodified.
//
// On success the current token is stored in the context under the key
// "csrf_token" so templates and JSON responses can read it.
func CSRFMiddleware(csrfInstance interface{}) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			verifier, ok := csrfInstance.(csrfVerifier)
			if !ok {
				return next(c)
			}

			// Only verify on state-changing methods
			switch c.Request.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				// safe methods — skip verification
			default:
				if err := verifier.VerifyToken(c.Request); err != nil {
					return c.Error(http.StatusForbidden, "Forbidden")
				}
			}

			// Store the token so downstream handlers/templates can use it
			c.Set("csrf_token", verifier.Token(c.Request))

			return next(c)
		}
	}
}
