// Package exceptions provides structured exception handling for Velocity.
// It includes rich error pages in development and safe error responses in production.
package exceptions

import (
	"fmt"
)

// Exception is the base interface for all exceptions.
type Exception interface {
	error
	GetMessage() string
	GetCode() int
	GetPrevious() error
	GetContext() map[string]any
}

// Reportable indicates if an exception should be logged.
type Reportable interface {
	ShouldReport() bool
}

// Renderable indicates an exception can render its own response.
type Renderable interface {
	Render(ctx RenderContext) error
}

// RenderContext provides the context needed for rendering exceptions.
type RenderContext interface {
	WriteHeader(statusCode int)
	Write(data []byte) (int, error)
	SetHeader(key, value string)
	GetHeader(key string) string
	RequestPath() string
	RequestMethod() string
	WantsJSON() bool
}

// BaseException provides a base implementation of the Exception interface.
type BaseException struct {
	message  string
	code     int
	previous error
	context  map[string]any
}

// NewBaseException creates a new BaseException.
func NewBaseException(message string, code int) *BaseException {
	return &BaseException{
		message: message,
		code:    code,
		context: make(map[string]any),
	}
}

// Error implements the error interface.
func (e *BaseException) Error() string {
	if e.previous != nil {
		return fmt.Sprintf("%s: %v", e.message, e.previous)
	}
	return e.message
}

// GetMessage returns the exception message.
func (e *BaseException) GetMessage() string {
	return e.message
}

// GetCode returns the exception code.
func (e *BaseException) GetCode() int {
	return e.code
}

// GetPrevious returns the previous/wrapped error.
func (e *BaseException) GetPrevious() error {
	return e.previous
}

// GetContext returns the exception context.
func (e *BaseException) GetContext() map[string]any {
	if e.context == nil {
		return make(map[string]any)
	}
	return e.context
}

// WithPrevious sets the previous error and returns the exception for chaining.
func (e *BaseException) WithPrevious(err error) *BaseException {
	e.previous = err
	return e
}

// WithContext adds context data and returns the exception for chaining.
func (e *BaseException) WithContext(key string, value any) *BaseException {
	if e.context == nil {
		e.context = make(map[string]any)
	}
	e.context[key] = value
	return e
}

// WithContextMap merges a map into the exception context.
func (e *BaseException) WithContextMap(ctx map[string]any) *BaseException {
	if e.context == nil {
		e.context = make(map[string]any)
	}
	for k, v := range ctx {
		e.context[k] = v
	}
	return e
}

// ShouldReport returns true by default - exceptions should be reported.
func (e *BaseException) ShouldReport() bool {
	return true
}

// Unwrap returns the previous error for errors.Is/errors.As support.
func (e *BaseException) Unwrap() error {
	return e.previous
}

// trigger
