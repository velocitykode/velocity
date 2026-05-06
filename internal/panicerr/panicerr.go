// Package panicerr provides a shared helper for converting recovered panic
// values into errors. All framework goroutines use this instead of ad-hoc
// panic-to-error conversion.
package panicerr

import (
	"errors"
	"fmt"
)

// Error is the typed representation of a recovered panic. It is returned as
// an `error` (so the existing `error` consumers keep working) but exposes the
// raw recovered value via `Recovered()` for callers that want to inspect or
// type-assert the original panic.
//
// `errors.Is(err, e)` works for the wrapped chain when the recovered value
// itself was an error.
type Error struct {
	value any
}

// New constructs a typed panic error from a recovered value. Pass the result
// of `recover()`. Returns nil if value is nil so callers can write
// `if err := panicerr.New(recover()); err != nil { ... }`.
func New(value any) *Error {
	if value == nil {
		return nil
	}
	return &Error{value: value}
}

// Recovered returns the raw value handed to recover(). Useful for callers
// that want to inspect the original panic (e.g. type-assert to a custom
// panic struct) rather than the formatted message.
func (e *Error) Recovered() any {
	if e == nil {
		return nil
	}
	return e.value
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if err, ok := e.value.(error); ok {
		return "panic: " + err.Error()
	}
	return fmt.Sprintf("panic: %v", e.value)
}

// Unwrap returns the underlying error if the recovered value was an error,
// so errors.Is / errors.As walk the chain.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	if err, ok := e.value.(error); ok {
		return err
	}
	return nil
}

// FromRecovered converts a recovered panic value into an error.
// If the recovered value is already an error, it is wrapped so that
// errors.Is / errors.As work on the original. Otherwise the value is
// formatted with %v.
//
// Returns a `*Error` typed as `error` so existing call sites that store
// the result in an `error` variable continue to work; new callers may
// type-assert to `*Error` (or use `errors.As`) to access `Recovered()`.
func FromRecovered(r any) error {
	if r == nil {
		return nil
	}
	return &Error{value: r}
}

// AsTyped extracts a *Error from any error value, returning nil if the error
// is not (and does not wrap) a panic-error.
func AsTyped(err error) *Error {
	var pe *Error
	if errors.As(err, &pe) {
		return pe
	}
	return nil
}
