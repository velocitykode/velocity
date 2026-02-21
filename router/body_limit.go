package router

import "net/http"

// bodyLimitKey is the context key set by BodyLimit middleware.
const bodyLimitKey = "_body_limit"

// BodyLimit returns middleware that limits request body size.
// If the body exceeds limit bytes, the handler receives an error
// from http.MaxBytesReader and the middleware returns 413.
func BodyLimit(limit int64) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Set(bodyLimitKey, limit)
			c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, limit)
			return next(c)
		}
	}
}
