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
