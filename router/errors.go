package router

import "net/http"

// HTTPError represents an HTTP error with a status code and message.
// Handlers can return this to control the response status and message
// when using a custom ErrorHandler.
type HTTPError struct {
	Code     int
	Message  string
	Internal error
}

// Error returns the error message.
func (e *HTTPError) Error() string {
	return e.Message
}

// Unwrap returns the internal error for errors.Is/As support.
func (e *HTTPError) Unwrap() error {
	return e.Internal
}

// NewHTTPError creates a new HTTPError. If no message is provided,
// the standard HTTP status text for the code is used.
func NewHTTPError(code int, message ...string) *HTTPError {
	msg := http.StatusText(code)
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &HTTPError{
		Code:    code,
		Message: msg,
	}
}

// ErrorHandlerMiddleware returns a middleware that routes errors from
// downstream handlers to fn. Install it per-group for layered error
// handling (JSON for /api, HTML for /, a tracing wrapper for all):
//
//	r.Group("/api", func(g router.Router) {
//	    g.Use(router.ErrorHandlerMiddleware(jsonErrorResponder))
//	    g.Get("/users", listUsers)
//	})
//
// When this middleware is active on a route, it absorbs the error and
// short-circuits the global Router.ErrorHandler for that route. fn is
// responsible for writing the response. Return value from the middleware
// is always nil so upstream middleware and the router's fallback do not
// re-fire for an already-handled error.
func ErrorHandlerMiddleware(fn func(c *Context, err error)) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if err := next(c); err != nil {
				fn(c, err)
			}
			return nil
		}
	}
}
