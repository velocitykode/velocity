// Package panicerr provides a shared helper for converting recovered panic
// values into errors. All framework goroutines use this instead of ad-hoc
// panic-to-error conversion.
package panicerr

import "fmt"

// FromRecovered converts a recovered panic value into an error.
// If the recovered value is already an error, it is wrapped with %w
// so that errors.Is / errors.As work on the original. Otherwise the
// value is formatted with %v.
func FromRecovered(r any) error {
	if e, ok := r.(error); ok {
		return fmt.Errorf("panic: %w", e)
	}
	return fmt.Errorf("panic: %v", r)
}
