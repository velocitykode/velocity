package validation

import (
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/validation/rules"
)

// RuleHandler defines a validation rule function. Canonical declaration lives
// in the stdlib-only contract leaf.
type RuleHandler = contract.RuleHandler

// RuleRegistry manages validation rules.
//
// The registry is safe for concurrent use: Register acquires a write lock and
// Get acquires a read lock. This matters because app.Services.Validator is a
// long-lived singleton shared across every handler goroutine, and adopters
// may call RegisterRule from lazily initialized code paths while requests are
// already running.
type RuleRegistry struct {
	mu    sync.RWMutex
	rules map[string]RuleHandler
}

// Register registers a validation rule.
// Panics with *contract.RegistrationError if handler is nil or a rule with the
// same name is already registered.
func (r *RuleRegistry) Register(name string, handler RuleHandler) {
	if handler == nil {
		panic(contract.NewRegistrationError("validation", fmt.Sprintf("nil handler for rule %q", name)))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rules[name]; exists {
		panic(contract.NewRegistrationError("validation", fmt.Sprintf("rule %q already registered", name)))
	}
	r.rules[name] = handler
}

// Get retrieves a validation rule handler
func (r *RuleRegistry) Get(name string) (RuleHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
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

	// Date and time rules
	reg.Register("date", rules.DateRule)
	reg.Register("date_format", rules.DateFormatRule)
	reg.Register("timezone", rules.TimezoneRule)

	// Network rules
	reg.Register("ip", rules.IPRule)
	reg.Register("ipv4", rules.IPv4Rule)
	reg.Register("ipv6", rules.IPv6Rule)

	// Format rules
	reg.Register("regex", rules.RegexRule)
	reg.Register("json", rules.JSONRule)
	reg.Register("uuid", rules.UUIDRule)
	reg.Register("ulid", rules.ULIDRule)

	// String prefix/suffix/password rules
	reg.Register("starts_with", rules.StartsWithRule)
	reg.Register("ends_with", rules.EndsWithRule)
	reg.Register("password", rules.PasswordRule)

	// Numeric comparison rules
	reg.Register("gt", rules.GtRule)
	reg.Register("gte", rules.GteRule)
	reg.Register("lt", rules.LtRule)
	reg.Register("lte", rules.LteRule)

	// File rules (require values to be *multipart.FileHeader or equivalent).
	reg.Register("file", rules.FileRule)
	reg.Register("mimes", rules.MimesRule)
	reg.Register("image", rules.ImageRule)
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
