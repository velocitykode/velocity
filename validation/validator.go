// Package validation provides Velocity's validation rules engine.
//
// Rules are typed values built by the constructors in this package
// (validation.Required(), validation.Min(8), ...) and collected in a
// validation.Rules set keyed by field name. Rule parameters are carried
// pre-split, so a parameter may contain any character.
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
	"strings"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Validator provides validation functionality. The canonical declaration
// lives in the stdlib-only contract leaf.
type Validator = contract.Validator

// ValidatedData contains validated and cleaned data. Canonical declaration
// (struct and methods) lives in the stdlib-only contract leaf.
type ValidatedData = contract.ValidatedData

// defaultValidator is the default validator implementation
type defaultValidator struct {
	registry *ruleRegistry
	messages Messages
	mu       sync.RWMutex
}

// defaultValidator must satisfy the contract Validator interface.
var _ contract.Validator = (*defaultValidator)(nil)

// NewValidator creates a new Validator instance.
func NewValidator() Validator {
	return newDefaultValidator()
}

// newDefaultValidator builds a validator with the built-in rules registered,
// keeping the concrete type for internal callers that need the normalized
// rule-set entry points.
func newDefaultValidator() *defaultValidator {
	v := &defaultValidator{
		registry: &ruleRegistry{
			rules: make(map[string]RuleHandler),
		},
		messages: make(Messages),
	}
	registerBuiltInRules(v.registry)
	return v
}

// Validate implements the Validator interface. It returns an error for a
// malformed rule set (wrapping ErrInvalidRule) and a ValidationErrors for
// field failures.
func (v *defaultValidator) Validate(data interface{}, rules Rules) (*ValidatedData, error) {
	normalized, err := normalizeRuleSet(rules)
	if err != nil {
		return nil, err
	}
	return v.validateNormalized(data, normalized)
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

// ValidateValue validates a single value against the given rules. The value
// has no field name, so messages and message-key lookups use "value".
func (v *defaultValidator) ValidateValue(value interface{}, rules ...Rule) error {
	normalized, err := normalizeRuleSet(Rules{"value": rules})
	if err != nil {
		return err
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	fieldRules := normalized.fields["value"]

	// Mirror the engine's "nullable" short-circuit: an empty value (nil or "")
	// with "nullable" skips every other rule. See nullableRule.
	if rulesContainNullable(fieldRules) && isNullableEmpty(value) {
		return nil
	}

	for _, r := range fieldRules {
		if err := v.validateField("value", value, r, nil, normalized.custom); err != nil {
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

// validateFieldRules applies one field's rules and records the value or the
// first failure on validated. Callers hold v.mu for reading. custom carries
// the handlers supplied by the rule set itself and is consulted before the
// registry.
func (v *defaultValidator) validateFieldRules(validated *ValidatedData, dataMap map[string]interface{}, field string, fieldRules []parsedRule, custom map[string]RuleHandler) {
	value := getFieldValue(dataMap, field)

	// "nullable" short-circuit: a field carrying "nullable" whose value is
	// empty (nil or "") skips ALL other rules - including "required" - and
	// is recorded as validated. See nullableRule for the semantics.
	if rulesContainNullable(fieldRules) && isNullableEmpty(value) {
		validated.Set(field, value)
		return
	}

	for _, rule := range fieldRules {
		if err := v.validateField(field, value, rule, dataMap, custom); err != nil {
			validated.AddError(field, err.Error(), rule.name)
			break // Stop on first error for this field
		}
	}

	// Add to validated data if no errors
	if !validated.Errors().HasError(field) {
		validated.Set(field, value)
	}
}

// validateField validates a single field against a rule. Handlers carried by
// the rule set win over the registry; normalization has already refused a
// carried name that shadows a framework rule, so the overlay cannot hijack a
// built-in.
func (v *defaultValidator) validateField(field string, value interface{}, rule parsedRule, data map[string]interface{}, custom map[string]RuleHandler) error {
	handler, exists := custom[rule.name]
	if !exists {
		handler, exists = v.registry.get(rule.name)
	}
	if !exists {
		return fmt.Errorf("unknown validation rule: %s", rule.name)
	}

	// rule.params is owned by the rule value that produced it and may be
	// shared across goroutines. Hand the handler its own copy so a custom
	// rule that mutates params in place cannot corrupt the rule set or race
	// another goroutine validating with it. No-param rules pay nothing.
	params := rule.params
	if len(params) > 0 {
		params = append([]string(nil), params...)
	}

	if err := handler(field, value, params, data); err != nil {
		if customMsg, ok := v.messages[MessageKey{Field: field, Rule: rule.name}]; ok {
			return fmt.Errorf("%s", customMsg)
		}
		return err
	}

	return nil
}

// parsedRule is one rule resolved to the form the engine evaluates.
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

// fieldExists reports whether a (possibly dotted) field path is present in
// data, walking nested map[string]interface{} segments. Unlike getFieldValue
// it distinguishes "missing" from "present with a nil value": a leaf key that
// exists with a nil value reports true. A missing key, or a non-map
// intermediate segment, reports false.
func fieldExists(data map[string]interface{}, field string) bool {
	parts := strings.Split(field, ".")
	current := data

	for i, part := range parts {
		if i == len(parts)-1 {
			_, ok := current[part]
			return ok
		}

		next, ok := current[part].(map[string]interface{})
		if !ok {
			return false
		}
		current = next
	}

	return false
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
