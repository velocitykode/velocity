package router

import "net/http"

// bodyLimitKey is the context key set by BodyLimit middleware.
const bodyLimitKey = "_body_limit"

// BodyLimit returns middleware that limits request body size to the given
// number of bytes. limit must be positive; a zero or negative value panics
// at construction time to prevent misconfiguration.
func BodyLimit(limit int64) MiddlewareFunc {
	if limit <= 0 {
		panic("router.BodyLimit: limit must be positive")
	}
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Set(bodyLimitKey, limit)
			c.Request.Body = http.MaxBytesReader(c.Response, c.Request.Body, limit)
			return next(c)
		}
	}
}
