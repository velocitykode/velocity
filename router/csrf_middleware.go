package router

import (
	"github.com/velocitykode/velocity/contract"
)

// CSRFMiddleware returns a MiddlewareFunc that runs the CSRF instance's
// Protect for EVERY request method, including safe methods (GET, HEAD,
// OPTIONS).
//
// Why no safe-method short-circuit here: Protect already distinguishes
// safe vs unsafe methods internally. On safe methods it (a) attaches the
// request-scoped CSRF token cache to r.Context() so downstream readers
// (template helpers, bond sharePropsFunc) can call
// csrf.TokenForRequest(r) and get a memoised, byte-identical token, and
// (b) writes the XSRF-TOKEN cookie for SPA clients. Short-circuiting safe
// methods here would bypass BOTH side effects, leaving
// csrf.TokenForRequest with no state to read (returns ErrNoTokenState)
// and the SPA with no XSRF cookie to echo. The downstream POST then 419s
// because the client sends a token the server never minted.
//
// The ORIGINAL *Context is reused (never Wrap): c.Request is replaced by
// the request Protect returns, so downstream reads see the augmented
// request while every other Context field survives. A rejection is
// returned unchanged for the error pipeline to render; nothing is written
// here.
//
// Usage: router.Use(router.CSRFMiddleware(app.CSRF))
func CSRFMiddleware(csrfInstance contract.CSRFProtector) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if csrfInstance == nil {
				return next(c)
			}
			r, err := csrfInstance.Protect(c.Response, c.Request)
			if r != nil {
				c.Request = r
			}
			if err != nil {
				return err
			}
			return next(c)
		}
	}
}
