package validation

import (
	"errors"
	"fmt"
	"strings"
)

// ErrValidationFailed is the sentinel returned (wrapped) whenever a
// validation.Result reports one or more field errors. Callers can use
// errors.Is(err, validation.ErrValidationFailed) to branch on the generic
// "validation failed" condition without inspecting per-field messages.
//
// Note: validation.Result.Err() returns an error that wraps this sentinel;
// the sentinel itself does not carry field messages — that data lives on
// Result/ValidationErrors.
var ErrValidationFailed = errors.New("velocity/validation: validation failed")

// ValidationErrors represents validation errors
type ValidationErrors struct {
	Errors map[string][]string
}

// Error implements the error interface
func (e ValidationErrors) Error() string {
	var parts []string
	for field, messages := range e.Errors {
		for _, msg := range messages {
			parts = append(parts, fmt.Sprintf("%s: %s", field, msg))
		}
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// HasError checks if a specific field has errors
func (e ValidationErrors) HasError(field string) bool {
	_, exists := e.Errors[field]
	return exists
}

// First returns the first error message for a field
func (e ValidationErrors) First(field string) string {
	if messages, ok := e.Errors[field]; ok && len(messages) > 0 {
		return messages[0]
	}
	return ""
}

// All returns all error messages
func (e ValidationErrors) All() map[string][]string {
	return e.Errors
}

// Count returns the total number of errors
func (e ValidationErrors) Count() int {
	count := 0
	for _, messages := range e.Errors {
		count += len(messages)
	}
	return count
}

// IsEmpty returns true if there are no errors
func (e ValidationErrors) IsEmpty() bool {
	return len(e.Errors) == 0
}

// addError adds an error message for a field
func (e *ValidationErrors) addError(field, message string) {
	if e.Errors == nil {
		e.Errors = make(map[string][]string)
	}
	e.Errors[field] = append(e.Errors[field], message)
}

// Merge merges another ValidationErrors into this one
func (e *ValidationErrors) Merge(other ValidationErrors) {
	if e.Errors == nil {
		e.Errors = make(map[string][]string)
	}
	for field, messages := range other.Errors {
		e.Errors[field] = append(e.Errors[field], messages...)
	}
}
