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
// fields were removed in the validation consolidation — use SetMessages to
// override any message the built-in rules emit.
type Validator interface {
	Validate(data interface{}, rules Rules) (*ValidatedData, error)
	ValidateRequest(r *http.Request, rules Rules) (*ValidatedData, error)
	ValidateValue(value interface{}, rule string) error
	RegisterRule(name string, handler RuleHandler)
	SetMessages(messages Messages)
}

// Rules defines validation rules for fields
type Rules map[string]string

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
		data:   make(map[string]interface{}),
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

	// Validate each field
	for field, ruleString := range rules {
		value := getFieldValue(dataMap, field)
		fieldRules := parseRules(ruleString)

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

// parseRules parses a rule string into individual rules
func parseRules(ruleString string) []parsedRule {
	var rules []parsedRule
	parts := strings.Split(ruleString, "|")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		colonIndex := strings.Index(part, ":")
		if colonIndex == -1 {
			rules = append(rules, parsedRule{name: part})
		} else {
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
