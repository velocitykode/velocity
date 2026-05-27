package router

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// CSRFMiddleware returns a MiddlewareFunc that delegates to the CSRF
// instance's exported Middleware method for EVERY request method,
// including safe methods (GET, HEAD, OPTIONS).
//
// Why no safe-method short-circuit here: csrf.Middleware already
// distinguishes safe vs unsafe methods internally. On safe methods it
// (a) attaches the request-scoped CSRF token cache to r.Context() so
// downstream readers (template helpers, bond sharePropsFunc) can call
// csrf.TokenForRequest(r) and get a memoised, byte-identical token,
// and (b) writes the XSRF-TOKEN cookie for SPA clients. Short-
// circuiting safe methods here bypasses BOTH side effects, leaving
// csrf.TokenForRequest with no state to read (returns ErrNoTokenState)
// and the SPA with no XSRF cookie to echo. The downstream POST then
// 419s because the client sends a token the server never minted.
//
// Pre-fix the adapter short-circuited safe methods, on the theory
// that csrf.Middleware "only validates", but csrf.Middleware does
// more than validate, so the short-circuit was incorrect.
//
// Usage: router.Use(router.CSRFMiddleware(app.CSRF))
func CSRFMiddleware(csrfInstance contract.CSRFProtector) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if csrfInstance == nil {
				return next(c)
			}

			var handlerErr error
			var called bool

			// Wrap the next handler as http.Handler so csrf.Middleware
			// can call it. Capture the request as csrf.Middleware
			// attached it (with the token-state context value) so
			// downstream router.Context reads see the augmented
			// request, not the original.
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				c.Request = r
				handlerErr = next(c)
			})

			csrfInstance.Middleware(inner).ServeHTTP(c.Response, c.Request)

			if !called {
				// CSRF middleware rejected the request (already wrote 419)
				return nil
			}

			return handlerErr
		}
	}
}
