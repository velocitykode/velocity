package exceptions

import (
	"net"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/internal/clientip"
)

// httpRenderContext adapts http.ResponseWriter to RenderContext.
type httpRenderContext struct {
	w       http.ResponseWriter
	r       *http.Request
	written bool
}

// newHTTPRenderContext creates a new httpRenderContext.
func newHTTPRenderContext(w http.ResponseWriter, r *http.Request) *httpRenderContext {
	return &httpRenderContext{w: w, r: r}
}

// WriteHeader writes the HTTP status code.
func (c *httpRenderContext) WriteHeader(statusCode int) {
	if !c.written {
		c.w.WriteHeader(statusCode)
		c.written = true
	}
}

// Write writes data to the response.
func (c *httpRenderContext) Write(data []byte) (int, error) {
	return c.w.Write(data)
}

// SetHeader sets a response header.
func (c *httpRenderContext) SetHeader(key, value string) {
	c.w.Header().Set(key, value)
}

// GetHeader gets a request header.
func (c *httpRenderContext) GetHeader(key string) string {
	return c.r.Header.Get(key)
}

// RequestPath returns the request path.
func (c *httpRenderContext) RequestPath() string {
	return c.r.URL.Path
}

// RequestMethod returns the request method.
func (c *httpRenderContext) RequestMethod() string {
	return c.r.Method
}

// WantsJSON returns true if the request prefers JSON response.
func (c *httpRenderContext) WantsJSON() bool {
	accept := c.r.Header.Get("Accept")
	contentType := c.r.Header.Get("Content-Type")
	xRequestedWith := c.r.Header.Get("X-Requested-With")

	// Check Accept header
	if strings.Contains(accept, "application/json") {
		return true
	}

	// Check Content-Type (for POST/PUT/PATCH requests)
	if strings.Contains(contentType, "application/json") {
		return true
	}

	// Check for AJAX requests
	if xRequestedWith == "XMLHttpRequest" {
		return true
	}

	// Check if path starts with /api
	if strings.HasPrefix(c.r.URL.Path, "/api") {
		return true
	}

	return false
}

// Middleware creates an HTTP middleware that handles exceptions and panics.
func Middleware(handler *Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := newHTTPRenderContext(w, r)

			defer func() {
				if recovered := recover(); recovered != nil {
					handler.HandlePanic(ctx, recovered)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// MiddlewareFunc creates an HTTP middleware function.
func MiddlewareFunc(handler *Handler) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx := newHTTPRenderContext(w, r)

			defer func() {
				if recovered := recover(); recovered != nil {
					handler.HandlePanic(ctx, recovered)
				}
			}()

			next(w, r)
		}
	}
}

// ErrorHandler creates an error handler function for use with routers that support error returns.
func ErrorHandler(handler *Handler) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		ctx := newHTTPRenderContext(w, r)

		exCtx := NewExceptionContext()
		exCtx.WithStackTrace(CaptureStackTrace(1))
		exCtx.URL = r.URL.Path
		exCtx.Method = r.Method
		exCtx.IP = getClientIP(r, handler.getTrustedProxies())
		exCtx.UserAgent = r.UserAgent()

		handler.Report(err, exCtx)
		handler.Render(ctx, err, exCtx)
	}
}

// getClientIP resolves the originating client IP for the exception
// audit trail via internal/clientip.Extract.
//
// Pre-fix this function honoured X-Forwarded-For / X-Real-IP
// unconditionally, taking the LEFT-MOST entry. Any direct-internet
// client could spoof the logged IP by setting the header, and a real
// proxy chain would surface the attacker-controlled prefix instead of
// the real client. That broke forensics (CWE-345) AND disagreed with
// the rate-limit path's right-most-of-trusted semantics, so the same
// request was attributed to two different IPs depending on which
// subsystem looked.
//
// Now: forwarded headers are honoured only when the direct peer
// (RemoteAddr) is in the configured trusted-proxy list; otherwise
// only RemoteAddr (port stripped) is used. The trust list is the
// process-wide deployment list installed on the Handler at boot via
// Handler.SetTrustedProxies, identical to the auth throttle layer.
//
// Returns "" only when RemoteAddr is unparseable and no usable header
// is present.
func getClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	if ip := clientip.ExtractString(r, trustedProxies); ip != "" {
		return ip
	}
	// Last-ditch fallback for completely-unparseable RemoteAddr (e.g.
	// hand-constructed test request with RemoteAddr=""). Strip a
	// trailing :port if present so we never accidentally log a port
	// number as the IP. Headers are NOT consulted here, the audit
	// trail records "unknown" rather than an attacker-controlled value.
	addr := r.RemoteAddr
	if colonIdx := strings.LastIndex(addr, ":"); colonIdx != -1 {
		addr = addr[:colonIdx]
	}
	return addr
}
