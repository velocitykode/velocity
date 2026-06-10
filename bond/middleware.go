package bond

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/router"
)

// Middleware returns HTTP middleware for Inertia protocol handling.
// It performs version checking, rewrites 302→303 for PUT/PATCH/DELETE,
// and redirects back on empty responses.
func (b *Bond) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.serveBuffered(w, r, next, func() bool { return false })
	})
}

// serveBuffered is the buffered core shared by Middleware and
// MiddlewareFunc. handlerErred reports whether the inner handler
// returned an error through a side channel the plain http.Handler
// signature cannot carry: when it did and the buffer holds an
// untouched empty 200, nothing is written to the real writer so the
// router's error path keeps full ownership of the response. Middleware
// has no error channel and passes a constant false.
func (b *Bond) serveBuffered(w http.ResponseWriter, r *http.Request, next http.Handler, handlerErred func() bool) {
	// Always add Vary for proper caching, preserving values set by
	// earlier middleware (CORS's Origin, security headers' Host).
	appendVary(w.Header(), "X-Inertia")

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

	// Empty 200 response. Two distinct cases: the handler errored
	// without writing, so leave the real writer untouched and let the
	// router error path respond; or the handler forgot to return
	// anything, in which case redirect back.
	if bw.statusCode == http.StatusOK && bw.buf.Len() == 0 {
		if handlerErred() {
			return
		}
		b.Back(w, r)
		return
	}

	// Rewrite 302 → 303 for PUT/PATCH/DELETE to prevent method resubmission
	if bw.statusCode == http.StatusFound && isSeeOtherMethod(r.Method) {
		bw.statusCode = http.StatusSeeOther
	}

	// Flush the buffered response to the real writer. A copy failure
	// almost always means the client connection went away (broken
	// pipe) so we surface it as a warning only; the response is
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
}

// MiddlewareFunc returns Velocity router middleware.
func (b *Bond) MiddlewareFunc() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			var handlerErr error
			orig := c.Response

			// Restore the real writer on every exit, including a
			// panic in next: the closure below points c.Response at
			// the response buffer, and the router's error path
			// (default or custom ErrorHandler, panic recovery
			// included) must write to the real connection, not an
			// abandoned buffer. The possibly-augmented c.Request is
			// kept.
			defer func() { c.Response = orig }()

			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c.Response = w
				c.Request = r
				handlerErr = next(c)
			})

			b.serveBuffered(orig, c.Request, handler,
				func() bool { return handlerErr != nil })

			return handlerErr
		}
	}
}

// appendVary adds v to the Vary header unless some earlier middleware
// already listed it, so repeated runs (Middleware then renderJSON) do
// not duplicate the entry and other middleware's cache keys survive.
func appendVary(h http.Header, v string) {
	for _, existing := range h.Values("Vary") {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), v) {
				return
			}
		}
	}
	h.Add("Vary", v)
}

// isSeeOtherMethod returns true for methods that should use 303 instead of 302.
func isSeeOtherMethod(method string) bool {
	return method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

// responseBuffer captures an HTTP response in memory so middleware can
// inspect and modify it before flushing to the real writer.
//
// The header map is a CLONE of the real writer's headers, not a shared
// reference. Sharing let every header the handler set leak onto the
// real connection even when the buffered body was discarded (handler
// error -> router error path, empty 200 -> redirect back), so an error
// response could carry the aborted handler's Content-Type, cache or
// cookie headers. With the clone, handler-set headers reach the wire
// only when flush commits the buffered response.
type responseBuffer struct {
	header     http.Header
	buf        bytes.Buffer
	statusCode int
}

func newResponseBuffer(w http.ResponseWriter) *responseBuffer {
	return &responseBuffer{
		header:     w.Header().Clone(),
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

// flush commits the buffered headers, status code, and body to the
// real writer. The buffered header map (a clone taken before the
// handler ran) replaces the real writer's map wholesale: keys the
// handler added or modified are copied over, and keys the handler
// deleted are removed. Returns the io.Copy error so callers can log
// closed-connection failures; the header has already been committed so
// nothing else is actionable.
func (rb *responseBuffer) flush(w http.ResponseWriter) error {
	dst := w.Header()
	for k := range dst {
		if _, ok := rb.header[k]; !ok {
			delete(dst, k)
		}
	}
	for k, v := range rb.header {
		dst[k] = v
	}
	w.WriteHeader(rb.statusCode)
	_, err := io.Copy(w, &rb.buf)
	return err
}
