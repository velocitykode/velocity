package exceptions

import (
	"net"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
)

// httpRenderContext is contract.NewRenderContext with this package's JSON
// heuristic (Accept, Content-Type, XHR, /api path).
type httpRenderContext struct {
	contract.RenderContext
}

// newHTTPRenderContext creates a new httpRenderContext.
func newHTTPRenderContext(w http.ResponseWriter, r *http.Request) *httpRenderContext {
	return &httpRenderContext{RenderContext: contract.NewRenderContext(w, r)}
}

// WantsJSON returns true if the request prefers JSON response.
func (c *httpRenderContext) WantsJSON() bool {
	r := c.Request()
	accept := r.Header.Get("Accept")
	contentType := r.Header.Get("Content-Type")
	xRequestedWith := r.Header.Get("X-Requested-With")

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
	if strings.HasPrefix(r.URL.Path, "/api") {
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

		exCtx := NewErrorContext()
		exCtx.WithStackTrace(contract.CaptureStackTrace(1))
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
