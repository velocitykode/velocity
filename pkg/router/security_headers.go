package router

// SecurityHeaders returns a middleware that sets common security response headers.
// These headers help protect against common web vulnerabilities like MIME sniffing,
// clickjacking, and information leakage.
func SecurityHeaders() MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.SetHeader("X-Content-Type-Options", "nosniff")
			c.SetHeader("X-Frame-Options", "DENY")
			c.SetHeader("Referrer-Policy", "strict-origin-when-cross-origin")
			c.SetHeader("X-XSS-Protection", "0")
			c.SetHeader("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			return next(c)
		}
	}
}
