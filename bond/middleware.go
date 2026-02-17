package bond

import (
	"bytes"
	"io"
	"net/http"

	"github.com/velocitykode/velocity/router"
)

// Middleware returns HTTP middleware for Inertia protocol handling.
// It performs version checking, rewrites 302→303 for PUT/PATCH/DELETE,
// and redirects back on empty responses.
func (b *Bond) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always set Vary header for proper caching
		w.Header().Set("Vary", "X-Inertia")

		// Non-Inertia requests pass through unbuffered
		if !isInertiaRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Check for version mismatch (pre-handler — skip wasted work)
		clientVersion := r.Header.Get("X-Inertia-Version")
		if clientVersion != "" && clientVersion != b.version {
			w.Header().Set("X-Inertia-Location", r.URL.String())
			w.WriteHeader(http.StatusConflict)
			return
		}

		// Buffer the response so we can inspect/modify it after the handler
		bw := newResponseBuffer(w)
		next.ServeHTTP(bw, r)

		// Empty 200 response — handler forgot to return anything, redirect back
		if bw.statusCode == http.StatusOK && bw.buf.Len() == 0 {
			b.Back(w, r)
			return
		}

		// Rewrite 302 → 303 for PUT/PATCH/DELETE to prevent method resubmission
		if bw.statusCode == http.StatusFound && isSeeOtherMethod(r.Method) {
			bw.statusCode = http.StatusSeeOther
		}

		// Flush the buffered response to the real writer
		bw.flush(w)
	})
}

// MiddlewareFunc returns Velocity router middleware.
func (b *Bond) MiddlewareFunc() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			var handlerErr error

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

// isSeeOtherMethod returns true for methods that should use 303 instead of 302.
func isSeeOtherMethod(method string) bool {
	return method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

// responseBuffer captures an HTTP response in memory so middleware can
// inspect and modify it before flushing to the real writer.
type responseBuffer struct {
	header     http.Header
	buf        bytes.Buffer
	statusCode int
}

func newResponseBuffer(w http.ResponseWriter) *responseBuffer {
	return &responseBuffer{
		header:     w.Header(),
		statusCode: http.StatusOK,
	}
}

func (rb *responseBuffer) Header() http.Header {
	return rb.header
}

func (rb *responseBuffer) Write(p []byte) (int, error) {
	return rb.buf.Write(p)
}

func (rb *responseBuffer) WriteHeader(code int) {
	rb.statusCode = code
}

// flush writes the buffered status code and body to the real writer.
// Headers are shared by pointer so they're already on w.
func (rb *responseBuffer) flush(w http.ResponseWriter) {
	w.WriteHeader(rb.statusCode)
	io.Copy(w, &rb.buf)
}
