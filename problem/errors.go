// Package problem is Velocity's error pipeline. A Handler reports an error
// once, through a gate of typed rules, and renders one response for it:
// application/problem+json (RFC 9457) when JSON is wanted, the configured
// error page or a reload for Inertia requests, and the error page or an HTML
// page otherwise. The
// constructors in this file build the framework's one HTTP error value,
// contract.HTTPError.
package problem

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// HTTPError is the framework's HTTP-shaped error value.
type HTTPError = contract.HTTPError

// BadRequest returns a 400 error. An optional message replaces the status
// text as the client-facing message.
func BadRequest(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusBadRequest, message...).WithOrigin(1)
}

// Unauthorized returns a 401 error.
func Unauthorized(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusUnauthorized, message...).WithOrigin(1)
}

// Forbidden returns a 403 error.
func Forbidden(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusForbidden, message...).WithOrigin(1)
}

// NotFound returns a 404 error.
func NotFound(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusNotFound, message...).WithOrigin(1)
}

// MethodNotAllowed returns a 405 error whose Allow header lists allowed.
// With no methods the header is omitted.
func MethodNotAllowed(allowed ...string) *HTTPError {
	e := contract.NewHTTPError(http.StatusMethodNotAllowed).WithOrigin(1)
	if len(allowed) > 0 {
		e.WithHeader("Allow", strings.Join(allowed, ", "))
	}
	return e
}

// Conflict returns a 409 error.
func Conflict(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusConflict, message...).WithOrigin(1)
}

// Gone returns a 410 error.
func Gone(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusGone, message...).WithOrigin(1)
}

// PayloadTooLarge returns a 413 error.
func PayloadTooLarge(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusRequestEntityTooLarge, message...).WithOrigin(1)
}

// TokenMismatch returns a 419 error for a missing or invalid CSRF token.
func TokenMismatch(message ...string) *HTTPError {
	if len(message) == 0 || message[0] == "" {
		message = []string{"CSRF token mismatch"}
	}
	return contract.NewHTTPError(contract.StatusTokenMismatch, message...).WithOrigin(1)
}

// TooManyRequests returns a 429 error. A positive retryAfter sets the
// Retry-After header in whole seconds, rounded up; zero or negative omits
// it.
func TooManyRequests(retryAfter time.Duration, message ...string) *HTTPError {
	return withRetryAfter(contract.NewHTTPError(http.StatusTooManyRequests, message...).WithOrigin(1), retryAfter)
}

// Internal returns a 500 error. Its message reaches clients only in debug
// mode; outside debug the status text is shown.
func Internal(message ...string) *HTTPError {
	return contract.NewHTTPError(http.StatusInternalServerError, message...).WithOrigin(1)
}

// ServiceUnavailable returns a 503 error. A positive retryAfter sets the
// Retry-After header in whole seconds, rounded up; zero or negative omits
// it.
func ServiceUnavailable(retryAfter time.Duration, message ...string) *HTTPError {
	return withRetryAfter(contract.NewHTTPError(http.StatusServiceUnavailable, message...).WithOrigin(1), retryAfter)
}

// withRetryAfter sets Retry-After on e when d is positive.
func withRetryAfter(e *HTTPError, d time.Duration) *HTTPError {
	if d <= 0 {
		return e
	}
	secs := int64(math.Ceil(d.Seconds()))
	return e.WithHeader("Retry-After", strconv.FormatInt(secs, 10))
}
