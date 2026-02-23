package router

import (
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// CSRFMiddleware returns a MiddlewareFunc that delegates to the CSRF
// instance's exported Middleware method.
//
// Usage: router.Use(router.CSRFMiddleware(app.CSRF))
func CSRFMiddleware(csrfInstance contract.CSRFProtector) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if csrfInstance == nil {
				return next(c)
			}

			// Only verify on state-changing methods
			switch c.Request.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				return next(c)
			}

			var handlerErr error
			var called bool

			// Wrap the next handler as http.Handler so csrf.Middleware can call it
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				// Update the context's request in case CSRF middleware modified it
				c.Request = r
				handlerErr = next(c)
			})

			csrfInstance.Middleware(inner).ServeHTTP(c.Response, c.Request)

			if !called {
				// CSRF middleware rejected the request (already wrote 403)
				return nil
			}

			return handlerErr
		}
	}
}
