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

		// Check for version mismatch, GET only.
		// POST/PUT/PATCH/DELETE skip this to avoid discarding form data with a 409.
		// The mutation processes normally, redirects, and the next GET catches it.
		if r.Method == http.MethodGet {
			clientVersion := r.Header.Get(HeaderVersion)
			if clientVersion != "" && clientVersion != b.version {
				// Defence-in-depth CRLF strip on the URL before
				// Header().Set. net/http rejects CR/LF at write time,
				// but a hostile or fuzzed request URI should never
				// reach a header-set sink raw. See stripCRLF.
				w.Header().Set(HeaderLocation, stripCRLF(r.URL.String()))
				w.WriteHeader(http.StatusConflict)
				return
			}
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

		// Flush the buffered response to the real writer. A copy failure
		// almost always means the client connection went away (broken
		// pipe) so we surface it as a warning only — the response is
		// already committed, nothing useful to do except log.
		if err := bw.flush(w); err != nil {
			b.mu.RLock()
			logger := b.logger
			b.mu.RUnlock()
			if logger != nil {
				logger.Warn("velocity/bond: flush buffered response failed",
					"err", err,
					"path", r.URL.Path,
				)
			}
		}
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
// Headers are shared by pointer so they're already on w. Returns the
// io.Copy error so callers can log closed-connection failures; the
// header has already been committed so nothing else is actionable.
func (rb *responseBuffer) flush(w http.ResponseWriter) error {
	w.WriteHeader(rb.statusCode)
	_, err := io.Copy(w, &rb.buf)
	return err
}
