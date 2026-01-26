package bond

import (
	"net/http"

	"github.com/velocitykode/velocity/pkg/router"
)

// Middleware returns HTTP middleware for Inertia protocol handling
// It performs version checking and sets appropriate headers
func (b *Bond) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always set Vary header for proper caching
		w.Header().Set("Vary", "X-Inertia")

		// Only apply version check to Inertia requests
		if !isInertiaRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for version mismatch
		clientVersion := r.Header.Get("X-Inertia-Version")
		if clientVersion != "" && clientVersion != b.version {
			// Version mismatch - force full page reload
			w.Header().Set("X-Inertia-Location", r.URL.String())
			w.WriteHeader(http.StatusConflict)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// MiddlewareFunc returns Velocity router middleware
func (b *Bond) MiddlewareFunc() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			var handlerErr error

			// Create a handler wrapper that captures the error
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c.Response = w
				c.Request = r
				handlerErr = next(c)
			})

			b.Middleware(handler).ServeHTTP(c.Response, c.Request)
			return handlerErr
		}
	}
}
