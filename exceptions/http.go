package exceptions

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HttpException represents an HTTP-specific exception with a status code.
type HttpException struct {
	*BaseException
	StatusCode int
	Headers    map[string]string
}

// NewHttpException creates a new HttpException.
func NewHttpException(statusCode int, message string) *HttpException {
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &HttpException{
		BaseException: NewBaseException(message, statusCode),
		StatusCode:    statusCode,
		Headers:       make(map[string]string),
	}
}

// WithHeader adds a header to the response.
func (e *HttpException) WithHeader(key, value string) *HttpException {
	if e.Headers == nil {
		e.Headers = make(map[string]string)
	}
	e.Headers[key] = value
	return e
}

// WithHeaders adds multiple headers to the response.
func (e *HttpException) WithHeaders(headers map[string]string) *HttpException {
	if e.Headers == nil {
		e.Headers = make(map[string]string)
	}
	for k, v := range headers {
		e.Headers[k] = v
	}
	return e
}

// GetStatusCode returns the HTTP status code.
func (e *HttpException) GetStatusCode() int {
	return e.StatusCode
}

// GetHeaders returns the response headers.
func (e *HttpException) GetHeaders() map[string]string {
	if e.Headers == nil {
		return make(map[string]string)
	}
	return e.Headers
}

// ShouldReport returns false for client errors (4xx), true for server errors (5xx).
func (e *HttpException) ShouldReport() bool {
	return e.StatusCode >= 500
}

// NotFoundHttpException represents a 404 Not Found error.
type NotFoundHttpException struct {
	*HttpException
}

// NewNotFoundHttpException creates a new 404 exception.
func NewNotFoundHttpException(message ...string) *NotFoundHttpException {
	msg := "Not Found"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &NotFoundHttpException{
		HttpException: NewHttpException(http.StatusNotFound, msg),
	}
}

// ShouldReport returns false - 404s typically shouldn't be logged as errors.
func (e *NotFoundHttpException) ShouldReport() bool {
	return false
}

// UnauthorizedHttpException represents a 401 Unauthorized error.
type UnauthorizedHttpException struct {
	*HttpException
}

// NewUnauthorizedHttpException creates a new 401 exception.
func NewUnauthorizedHttpException(message ...string) *UnauthorizedHttpException {
	msg := "Unauthorized"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &UnauthorizedHttpException{
		HttpException: NewHttpException(http.StatusUnauthorized, msg),
	}
}

// ShouldReport returns false - auth failures typically shouldn't be logged as errors.
func (e *UnauthorizedHttpException) ShouldReport() bool {
	return false
}

// ForbiddenHttpException represents a 403 Forbidden error.
type ForbiddenHttpException struct {
	*HttpException
}

// NewForbiddenHttpException creates a new 403 exception.
func NewForbiddenHttpException(message ...string) *ForbiddenHttpException {
	msg := "Forbidden"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &ForbiddenHttpException{
		HttpException: NewHttpException(http.StatusForbidden, msg),
	}
}

// ShouldReport returns false - forbidden errors typically shouldn't be logged as errors.
func (e *ForbiddenHttpException) ShouldReport() bool {
	return false
}

// ValidationException represents a 422 Unprocessable Entity error with validation errors.
type ValidationException struct {
	*HttpException
	ValidationErrors map[string][]string
}

// NewValidationException creates a new validation exception.
func NewValidationException(errors map[string][]string, message ...string) *ValidationException {
	msg := "The given data was invalid"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	e := &ValidationException{
		HttpException:    NewHttpException(http.StatusUnprocessableEntity, msg),
		ValidationErrors: errors,
	}
	e.WithContext("errors", errors)
	return e
}

// GetValidationErrors returns the validation errors.
func (e *ValidationException) GetValidationErrors() map[string][]string {
	return e.ValidationErrors
}

// ShouldReport returns false - validation errors shouldn't be logged.
func (e *ValidationException) ShouldReport() bool {
	return false
}

// Render implements Renderable for custom JSON response.
func (e *ValidationException) Render(ctx RenderContext) error {
	setJSONHeaders(ctx)
	ctx.WriteHeader(e.StatusCode)

	response := map[string]any{
		"message": e.GetMessage(),
		"errors":  e.ValidationErrors,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return err
	}

	_, err = ctx.Write(data)
	return err
}

// TooManyRequestsException represents a 429 Too Many Requests error.
type TooManyRequestsException struct {
	*HttpException
	RetryAfter int // Seconds until retry is allowed
}

// NewTooManyRequestsException creates a new 429 exception.
func NewTooManyRequestsException(retryAfter int, message ...string) *TooManyRequestsException {
	msg := "Too Many Requests"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	e := &TooManyRequestsException{
		HttpException: NewHttpException(http.StatusTooManyRequests, msg),
		RetryAfter:    retryAfter,
	}
	if retryAfter > 0 {
		e.WithHeader("Retry-After", fmt.Sprintf("%d", retryAfter))
	}
	return e
}

// ShouldReport returns false - rate limiting shouldn't be logged as errors.
func (e *TooManyRequestsException) ShouldReport() bool {
	return false
}

// ServiceUnavailableException represents a 503 Service Unavailable error.
type ServiceUnavailableException struct {
	*HttpException
	RetryAfter int // Seconds until service might be available
}

// NewServiceUnavailableException creates a new 503 exception.
func NewServiceUnavailableException(retryAfter int, message ...string) *ServiceUnavailableException {
	msg := "Service Unavailable"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	e := &ServiceUnavailableException{
		HttpException: NewHttpException(http.StatusServiceUnavailable, msg),
		RetryAfter:    retryAfter,
	}
	if retryAfter > 0 {
		e.WithHeader("Retry-After", fmt.Sprintf("%d", retryAfter))
	}
	return e
}

// MethodNotAllowedHttpException represents a 405 Method Not Allowed error.
type MethodNotAllowedHttpException struct {
	*HttpException
	AllowedMethods []string
}

// NewMethodNotAllowedHttpException creates a new 405 exception.
func NewMethodNotAllowedHttpException(allowedMethods []string, message ...string) *MethodNotAllowedHttpException {
	msg := "Method Not Allowed"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	e := &MethodNotAllowedHttpException{
		HttpException:  NewHttpException(http.StatusMethodNotAllowed, msg),
		AllowedMethods: allowedMethods,
	}
	if len(allowedMethods) > 0 {
		allowed := ""
		for i, m := range allowedMethods {
			if i > 0 {
				allowed += ", "
			}
			allowed += m
		}
		e.WithHeader("Allow", allowed)
	}
	return e
}

// ShouldReport returns false - method not allowed errors shouldn't be logged.
func (e *MethodNotAllowedHttpException) ShouldReport() bool {
	return false
}

// ConflictHttpException represents a 409 Conflict error.
type ConflictHttpException struct {
	*HttpException
}

// NewConflictHttpException creates a new 409 exception.
func NewConflictHttpException(message ...string) *ConflictHttpException {
	msg := "Conflict"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &ConflictHttpException{
		HttpException: NewHttpException(http.StatusConflict, msg),
	}
}

// ShouldReport returns false - conflict errors typically shouldn't be logged.
func (e *ConflictHttpException) ShouldReport() bool {
	return false
}

// GoneHttpException represents a 410 Gone error.
type GoneHttpException struct {
	*HttpException
}

// NewGoneHttpException creates a new 410 exception.
func NewGoneHttpException(message ...string) *GoneHttpException {
	msg := "Gone"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &GoneHttpException{
		HttpException: NewHttpException(http.StatusGone, msg),
	}
}

// ShouldReport returns false - gone errors shouldn't be logged.
func (e *GoneHttpException) ShouldReport() bool {
	return false
}

// BadRequestHttpException represents a 400 Bad Request error.
type BadRequestHttpException struct {
	*HttpException
}

// NewBadRequestHttpException creates a new 400 exception.
func NewBadRequestHttpException(message ...string) *BadRequestHttpException {
	msg := "Bad Request"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &BadRequestHttpException{
		HttpException: NewHttpException(http.StatusBadRequest, msg),
	}
}

// ShouldReport returns false - bad request errors shouldn't be logged.
func (e *BadRequestHttpException) ShouldReport() bool {
	return false
}

// InternalServerErrorException represents a 500 Internal Server Error.
type InternalServerErrorException struct {
	*HttpException
}

// NewInternalServerErrorException creates a new 500 exception.
func NewInternalServerErrorException(message ...string) *InternalServerErrorException {
	msg := "Internal Server Error"
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &InternalServerErrorException{
		HttpException: NewHttpException(http.StatusInternalServerError, msg),
	}
}

// Abort creates an HttpException with the given status code.
func Abort(statusCode int, message ...string) *HttpException {
	msg := ""
	if len(message) > 0 {
		msg = message[0]
	}
	return NewHttpException(statusCode, msg)
}

// AbortIf creates an HttpException if the condition is true.
func AbortIf(condition bool, statusCode int, message ...string) *HttpException {
	if condition {
		return Abort(statusCode, message...)
	}
	return nil
}

// AbortUnless creates an HttpException if the condition is false.
func AbortUnless(condition bool, statusCode int, message ...string) *HttpException {
	if !condition {
		return Abort(statusCode, message...)
	}
	return nil
}
