package validation

import (
	"net/http"
	"sort"
	"strings"
)

// Failure is the error a failed validation returns to the error pipeline.
// It names its status (422 Unprocessable Entity unless Status says
// otherwise), is never reported, and exposes the per-field messages through
// Errors, which the problem+json body carries as its "errors" member.
//
// The browser flow (errors and old input flashed, then a redirect) is
// rendered by the framework's default render rule for *Failure; Result
// supplies the messages and the input, RedirectTo and Bag shape that flow.
//
// errors.Is(err, ErrValidationFailed) and errors.As(err, &ValidationErrors{})
// both hold for a Failure whose Result has errors.
type Failure struct {
	// Result holds the per-field messages and the submitted input.
	Result *Result
	// Status is the HTTP status the failure answers with; zero means 422.
	Status int
	// RedirectTo is the same-origin path a browser is sent to after the
	// flash; empty sends it back to the previous page.
	RedirectTo string
	// Bag names the error bag the flashed errors are stored under; empty
	// stores them at the top level.
	Bag string
}

// NewFailure returns the Failure for result. A nil result yields a Failure
// with an empty Result.
func NewFailure(result *Result) *Failure {
	if result == nil {
		result = &Result{}
	}
	return &Failure{Result: result}
}

// Error returns "validation failed" followed by each field and its first
// message, fields in sorted order.
func (f *Failure) Error() string {
	messages := f.Errors()
	if len(messages) == 0 {
		return ErrValidationFailed.Error()
	}
	fields := make([]string, 0, len(messages))
	for field := range messages {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		msg := ""
		if msgs := messages[field]; len(msgs) > 0 {
			msg = msgs[0]
		}
		parts = append(parts, field+": "+msg)
	}
	return ErrValidationFailed.Error() + ": " + strings.Join(parts, "; ")
}

// StatusCode returns Status, or 422 when Status is zero.
func (f *Failure) StatusCode() int {
	if f.Status == 0 {
		return http.StatusUnprocessableEntity
	}
	return f.Status
}

// ShouldReport returns false: a validation failure is a client outcome.
func (f *Failure) ShouldReport() bool { return false }

// Errors returns the messages grouped by field, or nil when there is no
// Result.
func (f *Failure) Errors() map[string][]string {
	if f.Result == nil {
		return nil
	}
	return f.Result.Messages()
}

// ErrorBag returns the name of the error bag the flashed errors are stored
// under, or "" for the top level.
func (f *Failure) ErrorBag() string { return f.Bag }

// Unwrap returns the Result's error (see Result.Err), so the chain matches
// ErrValidationFailed and ValidationErrors. It is nil when the Result is nil
// or has no errors.
func (f *Failure) Unwrap() error {
	return f.Result.Err()
}
