package exceptions

import (
	"net/http"
	"strings"
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

// RecoverMiddleware is a simple panic recovery middleware that uses the global handler.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := newHTTPRenderContext(w, r)

		defer func() {
			if recovered := recover(); recovered != nil {
				Get().HandlePanic(ctx, recovered)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// ErrorHandler creates an error handler function for use with routers that support error returns.
func ErrorHandler(handler *Handler) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		ctx := newHTTPRenderContext(w, r)

		exCtx := NewExceptionContext()
		exCtx.WithStackTrace(CaptureStackTrace(1))
		exCtx.URL = r.URL.Path
		exCtx.Method = r.Method
		exCtx.IP = getClientIP(r)
		exCtx.UserAgent = r.UserAgent()

		handler.Report(err, exCtx)
		handler.Render(ctx, err, exCtx)
	}
}

// getClientIP extracts the client IP from the request.
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if colonIdx := strings.LastIndex(ip, ":"); colonIdx != -1 {
		ip = ip[:colonIdx]
	}
	return ip
}

// VelocityContextAdapter adapts a velocity router context to RenderContext.
// This is a generic implementation that can work with any context type
// that has the required methods.
type VelocityContextAdapter struct {
	responseWriter http.ResponseWriter
	request        *http.Request
	written        bool
}

// NewVelocityContextAdapter creates a new adapter from response writer and request.
func NewVelocityContextAdapter(w http.ResponseWriter, r *http.Request) *VelocityContextAdapter {
	return &VelocityContextAdapter{
		responseWriter: w,
		request:        r,
	}
}

// WriteHeader writes the HTTP status code.
func (a *VelocityContextAdapter) WriteHeader(statusCode int) {
	if !a.written {
		a.responseWriter.WriteHeader(statusCode)
		a.written = true
	}
}

// Write writes data to the response.
func (a *VelocityContextAdapter) Write(data []byte) (int, error) {
	return a.responseWriter.Write(data)
}

// SetHeader sets a response header.
func (a *VelocityContextAdapter) SetHeader(key, value string) {
	a.responseWriter.Header().Set(key, value)
}

// GetHeader gets a request header.
func (a *VelocityContextAdapter) GetHeader(key string) string {
	return a.request.Header.Get(key)
}

// RequestPath returns the request path.
func (a *VelocityContextAdapter) RequestPath() string {
	return a.request.URL.Path
}

// RequestMethod returns the request method.
func (a *VelocityContextAdapter) RequestMethod() string {
	return a.request.Method
}

// WantsJSON returns true if the request prefers JSON response.
func (a *VelocityContextAdapter) WantsJSON() bool {
	accept := a.request.Header.Get("Accept")
	contentType := a.request.Header.Get("Content-Type")
	xRequestedWith := a.request.Header.Get("X-Requested-With")

	if strings.Contains(accept, "application/json") {
		return true
	}
	if strings.Contains(contentType, "application/json") {
		return true
	}
	if xRequestedWith == "XMLHttpRequest" {
		return true
	}
	if strings.HasPrefix(a.request.URL.Path, "/api") {
		return true
	}

	return false
}
