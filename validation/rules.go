package validation

import (
	"fmt"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/validation/rules"
)

// RuleHandler defines a validation rule function
type RuleHandler func(field string, value interface{}, params []string, data map[string]interface{}) error

// RuleRegistry manages validation rules
type RuleRegistry struct {
	rules map[string]RuleHandler
}

// Register registers a validation rule.
// Panics with *contract.RegistrationError if handler is nil or a rule with the
// same name is already registered.
func (r *RuleRegistry) Register(name string, handler RuleHandler) {
	if handler == nil {
		panic(contract.NewRegistrationError("validation", fmt.Sprintf("nil handler for rule %q", name)))
	}
	if _, exists := r.rules[name]; exists {
		panic(contract.NewRegistrationError("validation", fmt.Sprintf("rule %q already registered", name)))
	}
	r.rules[name] = handler
}

// Get retrieves a validation rule handler
func (r *RuleRegistry) Get(name string) (RuleHandler, bool) {
	handler, exists := r.rules[name]
	return handler, exists
}

// registerBuiltInRules registers all built-in validation rules on the given registry.
func registerBuiltInRules(reg *RuleRegistry) {
	// Presence rules
	reg.Register("required", requiredRule)
	reg.Register("nullable", nullableRule)
	reg.Register("filled", filledRule)
	reg.Register("present", presentRule)

	// Type rules from rules package
	reg.Register("string", rules.StringRule)
	reg.Register("integer", rules.IntegerRule)
	reg.Register("numeric", rules.NumericRule)
	reg.Register("boolean", rules.BooleanRule)
	reg.Register("array", rules.ArrayRule)

	// String rules from rules package
	reg.Register("email", rules.EmailRule)
	reg.Register("url", rules.URLRule)
	reg.Register("url_public", rules.URLPublicRule)
	reg.Register("alpha", rules.AlphaRule)
	reg.Register("alpha_dash", rules.AlphaDashRule)
	reg.Register("alpha_num", rules.AlphaNumRule)

	// Size rules from rules package
	reg.Register("min", rules.MinRule)
	reg.Register("max", rules.MaxRule)
	reg.Register("size", rules.SizeRule)
	reg.Register("between", rules.BetweenRule)

	// Comparison rules from rules package
	reg.Register("same", rules.SameRule)
	reg.Register("different", rules.DifferentRule)
	reg.Register("in", rules.InRule)
	reg.Register("not_in", rules.NotInRule)
	reg.Register("confirmed", rules.ConfirmedRule)
	reg.Register("accepted", rules.AcceptedRule)

	// Conditional rules from rules package
	reg.Register("required_if", rules.RequiredIfRule)
	reg.Register("required_unless", rules.RequiredUnlessRule)
	reg.Register("required_with", rules.RequiredWithRule)
	reg.Register("required_without", rules.RequiredWithoutRule)
}

// requiredRule validates that a field is present and not empty
func requiredRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return fmt.Errorf("The %s field is required.", field)
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("The %s field is required.", field)
		}
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	case []string:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	case map[string]interface{}:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	}

	return nil
}

// nullableRule allows a field to be null
func nullableRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	// This rule always passes - it just marks a field as nullable
	return nil
}

// filledRule validates that if a field is present, it must not be empty
func filledRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil // Field not present, that's okay
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("The %s field must not be empty when present.", field)
		}
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("The %s field must not be empty when present.", field)
		}
	}

	return nil
}

// presentRule validates that a field is present (but may be empty)
func presentRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	// Check if field exists in data map
	if data != nil {
		if _, exists := data[field]; !exists {
			return fmt.Errorf("The %s field must be present.", field)
		}
	}
	return nil
}
