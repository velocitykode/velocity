package validation

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/pkg/validation/rules"
)

// RuleHandler defines a validation rule function
type RuleHandler func(field string, value interface{}, params []string, data map[string]interface{}) error

// RuleRegistry manages validation rules
type RuleRegistry struct {
	rules map[string]RuleHandler
	mu    sync.RWMutex
}

// global rule registry
var ruleRegistry = &RuleRegistry{
	rules: make(map[string]RuleHandler),
}

// Register registers a validation rule
func (r *RuleRegistry) Register(name string, handler RuleHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules[name] = handler
}

// Get retrieves a validation rule handler
func (r *RuleRegistry) Get(name string) (RuleHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, exists := r.rules[name]
	return handler, exists
}

// RegisterRule registers a custom validation rule globally
func RegisterRule(name string, handler RuleHandler) {
	ruleRegistry.Register(name, handler)
}

// init registers all built-in rules
func init() {
	registerBuiltInRules()
}

// registerBuiltInRules registers all built-in validation rules
func registerBuiltInRules() {
	// Presence rules
	RegisterRule("required", requiredRule)
	RegisterRule("nullable", nullableRule)
	RegisterRule("filled", filledRule)
	RegisterRule("present", presentRule)

	// Type rules from rules package
	RegisterRule("string", rules.StringRule)
	RegisterRule("integer", rules.IntegerRule)
	RegisterRule("numeric", rules.NumericRule)
	RegisterRule("boolean", rules.BooleanRule)
	RegisterRule("array", rules.ArrayRule)

	// String rules from rules package
	RegisterRule("email", rules.EmailRule)
	RegisterRule("url", rules.URLRule)
	RegisterRule("url_public", rules.URLPublicRule)
	RegisterRule("alpha", rules.AlphaRule)
	RegisterRule("alpha_dash", rules.AlphaDashRule)
	RegisterRule("alpha_num", rules.AlphaNumRule)

	// Size rules from rules package
	RegisterRule("min", rules.MinRule)
	RegisterRule("max", rules.MaxRule)
	RegisterRule("size", rules.SizeRule)
	RegisterRule("between", rules.BetweenRule)

	// Comparison rules from rules package
	RegisterRule("same", rules.SameRule)
	RegisterRule("different", rules.DifferentRule)
	RegisterRule("in", rules.InRule)
	RegisterRule("not_in", rules.NotInRule)
	RegisterRule("confirmed", rules.ConfirmedRule)
	RegisterRule("accepted", rules.AcceptedRule)
}

// requiredRule validates that a field is present and not empty
func requiredRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return fmt.Errorf("%s is required", field)
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("%s is required", field)
		}
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("%s is required", field)
		}
	case []string:
		if len(v) == 0 {
			return fmt.Errorf("%s is required", field)
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
			return fmt.Errorf("%s must not be empty when present", field)
		}
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("%s must not be empty when present", field)
		}
	}

	return nil
}

// presentRule validates that a field is present (but may be empty)
func presentRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	// Check if field exists in data map
	if data != nil {
		if _, exists := data[field]; !exists {
			return fmt.Errorf("%s must be present", field)
		}
	}
	return nil
}
