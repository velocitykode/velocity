package validation

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/velocitykode/velocity/orm"
)

// Check validates request data against the given rules.
// It extracts form values or JSON body from the request automatically.
func Check(r *http.Request, rules Rules, messages ...Messages) *Result {
	data := ExtractRequestData(r)
	return run(data, rules, nil, messages...)
}

// CheckData validates a pre-extracted data map against rules.
func CheckData(data map[string]interface{}, rules Rules, messages ...Messages) *Result {
	return run(data, rules, nil, messages...)
}

// CheckWithDB validates request data with database rules (unique, exists) available.
func CheckWithDB(r *http.Request, rules Rules, db orm.Database, messages ...Messages) *Result {
	data := ExtractRequestData(r)
	return run(data, rules, db, messages...)
}

// CheckDataWithDB validates a data map with database rules available.
func CheckDataWithDB(data map[string]interface{}, rules Rules, db orm.Database, messages ...Messages) *Result {
	return run(data, rules, db, messages...)
}

// Result holds the outcome of a validation check.
// It carries both error messages and the original input data so that
// view helpers can re-populate forms with old values automatically.
type Result struct {
	errors map[string][]string
	input  map[string]interface{}
}

// HasErrors returns true if validation failed.
func (r *Result) HasErrors() bool {
	return len(r.errors) > 0
}

// First returns the first error message for a field, or "".
func (r *Result) First(field string) string {
	if msgs, ok := r.errors[field]; ok && len(msgs) > 0 {
		return msgs[0]
	}
	return ""
}

// All returns the first error per field as a flat map.
// This matches the Inertia convention where errors is { field: "message" }.
func (r *Result) All() map[string]string {
	result := make(map[string]string, len(r.errors))
	for field, msgs := range r.errors {
		if len(msgs) > 0 {
			result[field] = msgs[0]
		}
	}
	return result
}

// Messages returns all error messages grouped by field.
func (r *Result) Messages() map[string][]string {
	return r.errors
}

// Err returns nil when validation passed, or an error wrapping
// ErrValidationFailed when one or more fields failed. The returned error
// also satisfies errors.As(&ValidationErrors{}) so callers can access the
// per-field message map.
func (r *Result) Err() error {
	if r == nil || !r.HasErrors() {
		return nil
	}
	ve := ValidationErrors{Errors: r.errors}
	return &resultError{ve: ve}
}

// resultError wraps ValidationErrors and ErrValidationFailed so both
// errors.Is(err, ErrValidationFailed) and errors.As(&ValidationErrors{})
// succeed.
type resultError struct {
	ve ValidationErrors
}

func (e *resultError) Error() string { return e.ve.Error() }
func (e *resultError) Unwrap() error { return ErrValidationFailed }
func (e *resultError) As(target interface{}) bool {
	if p, ok := target.(*ValidationErrors); ok {
		*p = e.ve
		return true
	}
	return false
}

// Old returns the original input data with sensitive fields removed.
// Password, secret, and token fields are stripped automatically (case-insensitive).
func (r *Result) Old() map[string]interface{} {
	old := make(map[string]interface{}, len(r.input))
	for k, v := range r.input {
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

// run validates data against rules using a fresh validator.
func run(data map[string]interface{}, rules Rules, db orm.Database, messages ...Messages) *Result {
	v := NewValidator()

	// Register database rules when DB is available
	if db != nil {
		v.RegisterRule("unique", UniqueRule(db))
		v.RegisterRule("exists", ExistsRule(db))
	}

	if len(messages) > 0 {
		v.SetMessages(messages[0])
	}

	result := &Result{input: data}

	_, err := v.Validate(data, rules)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			result.errors = ve.Errors
		}
	}

	return result
}

// ExtractRequestData reads form values or JSON body from the request.
func ExtractRequestData(r *http.Request) map[string]interface{} {
	ct := r.Header.Get("Content-Type")

	// Try JSON first for application/json
	if strings.HasPrefix(ct, "application/json") {
		var data map[string]interface{}
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB limit
		if err == nil && len(body) > 0 {
			// Restore body so ctx.Bind() can read it again
			r.Body = io.NopCloser(bytes.NewReader(body))
			if json.Unmarshal(body, &data) == nil {
				return data
			}
		}
	}

	// Fall back to form data (idempotent after first parse)
	if err := r.ParseForm(); err == nil && len(r.Form) > 0 {
		data := make(map[string]interface{}, len(r.Form))
		for key, values := range r.Form {
			if len(values) == 1 {
				data[key] = values[0]
			} else {
				data[key] = values
			}
		}
		return data
	}

	return make(map[string]interface{})
}
