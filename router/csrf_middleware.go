package router

import (
	"net/http"
)

// csrfMiddlewarer is satisfied by *csrf.CSRF which exports Middleware() and
// RouterMiddleware(). We use Middleware(http.Handler) to avoid importing csrf.
type csrfMiddlewarer interface {
	Middleware(next http.Handler) http.Handler
}

// CSRFMiddleware returns a MiddlewareFunc that delegates to the CSRF
// instance's exported Middleware method. The csrfInstance must satisfy
// the Middleware(http.Handler) http.Handler interface (e.g. *csrf.CSRF).
//
// Usage: router.Use(router.CSRFMiddleware(app.CSRF))
func CSRFMiddleware(csrfInstance interface{}) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			m, ok := csrfInstance.(csrfMiddlewarer)
			if !ok {
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

			m.Middleware(inner).ServeHTTP(c.Response, c.Request)

			if !called {
				// CSRF middleware rejected the request (already wrote 403)
				return nil
			}

			return handlerErr
		}
	}
}
