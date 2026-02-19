package validate

import "strings"

// Errors holds the result of a validation check.
// It carries both error messages and the original input data so that
// view helpers can re-populate forms with old values automatically.
type Errors struct {
	errors map[string][]string
	input  map[string]interface{}
}

// HasErrors returns true if validation failed.
func (e *Errors) HasErrors() bool {
	return len(e.errors) > 0
}

// First returns the first error message for a field, or "".
func (e *Errors) First(field string) string {
	if msgs, ok := e.errors[field]; ok && len(msgs) > 0 {
		return msgs[0]
	}
	return ""
}

// All returns the first error per field as a flat map.
// This matches the Inertia convention where errors is { field: "message" }.
func (e *Errors) All() map[string]string {
	result := make(map[string]string, len(e.errors))
	for field, msgs := range e.errors {
		if len(msgs) > 0 {
			result[field] = msgs[0]
		}
	}
	return result
}

// Messages returns all error messages grouped by field.
func (e *Errors) Messages() map[string][]string {
	return e.errors
}

// Old returns the original input data with sensitive fields removed.
// Password, secret, and token fields are stripped automatically.
func (e *Errors) Old() map[string]interface{} {
	old := make(map[string]interface{}, len(e.input))
	for k, v := range e.input {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "password") ||
			strings.Contains(lower, "secret") ||
			strings.Contains(lower, "token") {
			continue
		}
		old[k] = v
	}
	return old
}
