package contract

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Validator provides validation functionality.
//
// Note: Velocity ships English-only messages. Historical SetLocale/Locale
// fields were removed in the validation consolidation, use SetMessages to
// override any message the built-in rules emit.
type Validator interface {
	Validate(data interface{}, rules ValidationRules) (*ValidatedData, error)
	ValidateRequest(r *http.Request, rules ValidationRules) (*ValidatedData, error)
	ValidateValue(value interface{}, rule string) error
	RegisterRule(name string, handler RuleHandler)
	SetMessages(messages ValidationMessages)
}

// ValidationRules defines validation rules per field. It is the canonical
// adopter facing type and matches the shape returned by vform.FormRequest.Rules():
// each field maps to a slice of individual rule strings.
//
//	rules := validation.Rules{
//	    "email":    {"required", "email"},
//	    "password": {"required", "min:8", "confirmed"},
//	}
//
// ValidationRules is a type alias (not a defined type) so that adopter methods
// declared with the underlying map type, e.g.
//
//	func (r *RegisterRequest) Rules() map[string][]string { ... }
//
// still satisfy interfaces declared against it (notably vform.FormRequest).
// Using a defined type here would cause those methods to silently fail the
// interface assertion in vform, skipping validation entirely.
type ValidationRules = map[string][]string

// ValidationMessages defines custom error messages.
type ValidationMessages = map[string]string

// RuleHandler defines a validation rule function.
type RuleHandler func(field string, value interface{}, params []string, data map[string]interface{}) error

// ValidatedData contains validated and cleaned data.
type ValidatedData struct {
	data   map[string]interface{}
	errors ValidationErrors
}

// NewValidatedData returns a ValidatedData with its data map and error indexes
// initialized, ready for a validator engine to populate via Set and AddError.
func NewValidatedData() *ValidatedData {
	return &ValidatedData{
		data: make(map[string]interface{}),
		errors: ValidationErrors{
			Errors:       make(map[string][]string),
			RulesByField: make(map[string][]string),
		},
	}
}

// Set stores a validated value by key.
func (v *ValidatedData) Set(key string, value interface{}) {
	v.data[key] = value
}

// AddError records a validation error for a field. rule names the rule that
// failed; pass "" when the source has no rule.
func (v *ValidatedData) AddError(field, message, rule string) {
	v.errors.addError(field, message, rule)
}

// Get retrieves a validated value by key
func (v *ValidatedData) Get(key string) interface{} {
	return v.data[key]
}

// GetString retrieves a string value
func (v *ValidatedData) GetString(key string) string {
	if val, ok := v.data[key].(string); ok {
		return val
	}
	return ""
}

// GetInt retrieves an integer value
func (v *ValidatedData) GetInt(key string) int {
	switch val := v.data[key].(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		if intVal, err := strconv.Atoi(val); err == nil {
			return intVal
		}
	}
	return 0
}

// GetBool retrieves a boolean value
func (v *ValidatedData) GetBool(key string) bool {
	if val, ok := v.data[key].(bool); ok {
		return val
	}
	return false
}

// All returns all validated data
func (v *ValidatedData) All() map[string]interface{} {
	return v.data
}

// HasErrors returns true if validation failed
func (v *ValidatedData) HasErrors() bool {
	return len(v.errors.Errors) > 0
}

// Errors returns validation errors
func (v *ValidatedData) Errors() ValidationErrors {
	return v.errors
}

// ErrValidationFailed is the sentinel returned (wrapped) whenever a
// validation.Result reports one or more field errors. Callers can use
// errors.Is(err, validation.ErrValidationFailed) to branch on the generic
// "validation failed" condition without inspecting per-field messages.
//
// Note: validation.Result.Err() returns an error that wraps this sentinel;
// the sentinel itself does not carry field messages -- that data lives on
// Result/ValidationErrors.
var ErrValidationFailed = errors.New("velocity/validation: validation failed")

// ValidationErrors represents validation errors.
//
// Errors maps field -> message(s) and is the user-facing shape carried into
// flash cookies, JSON responses, and view data.
//
// RulesByField is a parallel index mapping field -> rule-name(s) that
// produced each error, in the same order as Errors[field]. Tests should
// prefer this over substring-matching the message. It is populated whenever
// addError is called with a non-empty rule name.
type ValidationErrors struct {
	Errors       map[string][]string
	RulesByField map[string][]string
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

// addError adds an error message for a field. rule names the rule that
// failed; pass "" when the source has no rule (Merge, external input).
func (e *ValidationErrors) addError(field, message, rule string) {
	if e.Errors == nil {
		e.Errors = make(map[string][]string)
	}
	if e.RulesByField == nil {
		e.RulesByField = make(map[string][]string)
	}
	e.Errors[field] = append(e.Errors[field], message)
	e.RulesByField[field] = append(e.RulesByField[field], rule)
}

// HasRule reports whether field failed the named rule.
func (e ValidationErrors) HasRule(field, rule string) bool {
	for _, r := range e.RulesByField[field] {
		if r == rule {
			return true
		}
	}
	return false
}

// RulesFor returns the rule names that produced errors for field, in the
// same order as Errors[field].
func (e ValidationErrors) RulesFor(field string) []string {
	return append([]string(nil), e.RulesByField[field]...)
}

// Merge merges another ValidationErrors into this one
func (e *ValidationErrors) Merge(other ValidationErrors) {
	if e.Errors == nil {
		e.Errors = make(map[string][]string)
	}
	if e.RulesByField == nil {
		e.RulesByField = make(map[string][]string)
	}
	for field, messages := range other.Errors {
		e.Errors[field] = append(e.Errors[field], messages...)
	}
	for field, rules := range other.RulesByField {
		e.RulesByField[field] = append(e.RulesByField[field], rules...)
	}
}
