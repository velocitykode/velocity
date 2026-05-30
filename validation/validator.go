// Package validation provides Velocity's validation rules engine.
//
// The core package depends only on contract, validation/rules, and the
// standard library; it carries no orm or SQL-driver dependency. DB-backed
// rules (unique, exists) and the driver-error mapper (AsValidationError)
// live in the validation/dbrules subpackage, which imports orm and the SQL
// drivers and wires its handlers into the core engine via CheckWithRulesW /
// CheckDataWithRules.
package validation

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Validator provides validation functionality.
//
// Note: Velocity ships English-only messages. Historical SetLocale/Locale
// fields were removed in the validation consolidation, use SetMessages to
// override any message the built-in rules emit.
type Validator interface {
	Validate(data interface{}, rules Rules) (*ValidatedData, error)
	ValidateRequest(r *http.Request, rules Rules) (*ValidatedData, error)
	ValidateValue(value interface{}, rule string) error
	RegisterRule(name string, handler RuleHandler)
	SetMessages(messages Messages)
}

// Rules defines validation rules per field. Rules is the canonical adopter
// facing type and matches the shape returned by vform.FormRequest.Rules():
// each field maps to a slice of individual rule strings.
//
//	rules := validation.Rules{
//	    "email":    {"required", "email"},
//	    "password": {"required", "min:8", "confirmed"},
//	}
//
// Authoring rules in pipe-string form (e.g. "required|email") is still
// supported via PipeRules and the NewRules() helper, which converts a
// PipeRules value into the canonical Rules type:
//
//	rules := validation.NewRules(validation.PipeRules{
//	    "email": "required|email",
//	})
//
// Pipe-delimited tokens inside a single slice element are accepted for
// backward compatibility; the validator splits on '|' before evaluating.
//
// Rules is a type alias (not a defined type) so that adopter methods
// declared with the underlying map type, e.g.
//
//	func (r *RegisterRequest) Rules() map[string][]string { ... }
//
// still satisfy interfaces declared against validation.Rules (notably
// vform.FormRequest). Using a defined type here would cause those
// methods to silently fail the interface assertion in vform, skipping
// validation entirely.
type Rules = map[string][]string

// PipeRules is the legacy pipe-string form of validation rules. Each field
// maps to a single string of '|'-delimited rule tokens. Convert to the
// canonical Rules type with NewRules() before passing to a validator.
type PipeRules map[string]string

// NewRules converts a PipeRules (legacy "required|email" form) into the
// canonical slice-of-rules Rules type. Empty tokens are dropped.
func NewRules(p PipeRules) Rules {
	if p == nil {
		return nil
	}
	out := make(Rules, len(p))
	for field, pipe := range p {
		out[field] = splitPipe(pipe)
	}
	return out
}

// splitPipe splits a "required|min:3" string into ["required", "min:3"],
// trimming whitespace and dropping empty tokens. Exposed as a package-private
// helper so both NewRules() and the validator's internal rule parser share
// one implementation.
func splitPipe(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Messages defines custom error messages
type Messages map[string]string

// ValidatedData contains validated and cleaned data
type ValidatedData struct {
	data   map[string]interface{}
	errors ValidationErrors
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

// defaultValidator is the default validator implementation
type defaultValidator struct {
	registry *RuleRegistry
	messages Messages
	mu       sync.RWMutex
}

// NewValidator creates a new Validator instance.
func NewValidator() Validator {
	v := &defaultValidator{
		registry: &RuleRegistry{
			rules: make(map[string]RuleHandler),
		},
		messages: make(Messages),
	}
	registerBuiltInRules(v.registry)
	return v
}

// RegisterRule registers a custom validation rule on this validator instance.
func (v *defaultValidator) RegisterRule(name string, handler RuleHandler) {
	v.registry.Register(name, handler)
}

// Validate implements the Validator interface
func (v *defaultValidator) Validate(data interface{}, rules Rules) (*ValidatedData, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	validated := &ValidatedData{
		data: make(map[string]interface{}),
		errors: ValidationErrors{
			Errors:       make(map[string][]string),
			RulesByField: make(map[string][]string),
		},
	}

	// Convert data to map
	dataMap, err := toMap(data)
	if err != nil {
		return nil, fmt.Errorf("failed to convert data to map: %w", err)
	}

	// Validate each field. Rules is map[string][]string; each slice element
	// may itself be a pipe-delimited string ("required|min:3") for backward
	// compatibility with the legacy PipeRules form. parseRuleSlice flattens
	// both shapes into a single ordered list of parsedRule values.
	for field, fieldRuleStrings := range rules {
		value := getFieldValue(dataMap, field)
		fieldRules := parseRuleSlice(fieldRuleStrings)

		for _, rule := range fieldRules {
			if err := v.validateField(field, value, rule, dataMap); err != nil {
				validated.errors.addError(field, err.Error(), rule.name)
				break // Stop on first error for this field
			}
		}

		// Add to validated data if no errors
		if !validated.errors.HasError(field) {
			validated.data[field] = value
		}
	}

	if validated.HasErrors() {
		return validated, validated.errors
	}

	return validated, nil
}

// ValidateRequest validates an HTTP request
func (v *defaultValidator) ValidateRequest(r *http.Request, rules Rules) (*ValidatedData, error) {
	// Parse form data
	if err := r.ParseForm(); err != nil {
		return nil, fmt.Errorf("failed to parse form: %w", err)
	}

	// Convert form values to map
	data := make(map[string]interface{})
	for key, values := range r.Form {
		if len(values) == 1 {
			data[key] = values[0]
		} else {
			data[key] = values
		}
	}

	return v.Validate(data, rules)
}

// ValidateValue validates a single value against a rule
func (v *defaultValidator) ValidateValue(value interface{}, rule string) error {
	v.mu.RLock()
	defer v.mu.RUnlock()

	fieldRules := parseRules(rule)
	for _, r := range fieldRules {
		if err := v.validateField("value", value, r, nil); err != nil {
			return err
		}
	}
	return nil
}

// SetMessages sets custom error messages
func (v *defaultValidator) SetMessages(messages Messages) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.messages = messages
}

// validateField validates a single field against a rule
func (v *defaultValidator) validateField(field string, value interface{}, rule parsedRule, data map[string]interface{}) error {
	handler, exists := v.registry.Get(rule.name)
	if !exists {
		return fmt.Errorf("unknown validation rule: %s", rule.name)
	}

	if err := handler(field, value, rule.params, data); err != nil {
		// Check for custom message
		messageKey := fmt.Sprintf("%s.%s", field, rule.name)
		if customMsg, ok := v.messages[messageKey]; ok {
			return fmt.Errorf("%s", customMsg)
		}
		return err
	}

	return nil
}

// parseRules parses a single pipe-delimited rule string into parsedRule
// values. Retained for ValidateValue and any internal callers that still
// receive a single string. New code should prefer parseRuleSlice which
// accepts the canonical Rules slice form.
func parseRules(ruleString string) []parsedRule {
	return parseRuleSlice(splitPipe(ruleString))
}

// parseRuleSlice parses an ordered slice of rule tokens into parsedRule
// values. Each token may itself contain '|' (legacy pipe-string form) and
// is re-split before parsing. Empty / whitespace-only tokens are dropped.
func parseRuleSlice(tokens []string) []parsedRule {
	var rules []parsedRule
	for _, raw := range tokens {
		// Each slice element may itself be a pipe-delimited compound rule.
		for _, part := range splitPipe(raw) {
			colonIndex := strings.Index(part, ":")
			if colonIndex == -1 {
				rules = append(rules, parsedRule{name: part})
				continue
			}
			name := part[:colonIndex]
			paramString := part[colonIndex+1:]
			params := strings.Split(paramString, ",")
			rules = append(rules, parsedRule{name: name, params: params})
		}
	}
	return rules
}

// parsedRule represents a parsed validation rule
type parsedRule struct {
	name   string
	params []string
}

// getFieldValue retrieves a field value from a map (supports nested fields)
func getFieldValue(data map[string]interface{}, field string) interface{} {
	// Handle nested fields (e.g., "address.city")
	parts := strings.Split(field, ".")
	current := data

	for i, part := range parts {
		if i == len(parts)-1 {
			return current[part]
		}

		if next, ok := current[part].(map[string]interface{}); ok {
			current = next
		} else {
			return nil
		}
	}

	return nil
}

// toMap converts various data types to map[string]interface{}
func toMap(data interface{}) (map[string]interface{}, error) {
	switch d := data.(type) {
	case map[string]interface{}:
		return d, nil
	case map[string]string:
		result := make(map[string]interface{})
		for k, v := range d {
			result[k] = v
		}
		return result, nil
	default:
		return nil, fmt.Errorf("unsupported data type: %T", data)
	}
}
