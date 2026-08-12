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

// ruleRegistry holds the handlers one validator can resolve by name: the
// built-ins, plus the DB-backed handlers a Check helper installs for the
// duration of one run. Rules an adopter writes carry their own handler
// (see Custom), so nothing outside this package registers into it.
//
// The registry is safe for concurrent use: register acquires a write lock
// and get acquires a read lock. This matters because app.Services.Validator
// is a long-lived singleton shared across every handler goroutine.
type ruleRegistry struct {
	mu    sync.RWMutex
	rules map[string]RuleHandler
}

// mustRegister registers a built-in rule during validator construction. A
// failure here is a framework defect in the built-in table, not adopter
// input, so it is unrecoverable.
func mustRegister(r *ruleRegistry, name string, handler RuleHandler) {
	if err := r.register(name, handler); err != nil {
		panic(err)
	}
}

// register reports a nil handler or a name that is already taken. It runs on
// caller-supplied handlers (the Check helpers' extra map), so it reports
// rather than panics.
func (r *ruleRegistry) register(name string, handler RuleHandler) error {
	if handler == nil {
		return contract.NewRegistrationError("validation", fmt.Sprintf("nil handler for rule %q", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rules[name]; exists {
		return contract.NewRegistrationError("validation", fmt.Sprintf("rule %q already registered", name))
	}
	r.rules[name] = handler
	return nil
}

// get retrieves a validation rule handler
func (r *ruleRegistry) get(name string) (RuleHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	handler, exists := r.rules[name]
	return handler, exists
}

// registerBuiltInRules registers all built-in validation rules on the given registry.
func registerBuiltInRules(reg *ruleRegistry) {
	// Presence rules
	mustRegister(reg, "required", requiredRule)
	mustRegister(reg, "nullable", nullableRule)
	mustRegister(reg, "filled", filledRule)
	mustRegister(reg, "present", presentRule)

	// Type rules from rules package
	mustRegister(reg, "string", rules.StringRule)
	mustRegister(reg, "integer", rules.IntegerRule)
	mustRegister(reg, "numeric", rules.NumericRule)
	mustRegister(reg, "boolean", rules.BooleanRule)
	mustRegister(reg, "array", rules.ArrayRule)

	// String rules from rules package
	mustRegister(reg, "email", rules.EmailRule)
	mustRegister(reg, "url", rules.URLRule)
	mustRegister(reg, "url_public", rules.URLPublicRule)
	mustRegister(reg, "alpha", rules.AlphaRule)
	mustRegister(reg, "alpha_dash", rules.AlphaDashRule)
	mustRegister(reg, "alpha_num", rules.AlphaNumRule)

	// Size rules from rules package
	mustRegister(reg, "min", rules.MinRule)
	mustRegister(reg, "max", rules.MaxRule)
	mustRegister(reg, "size", rules.SizeRule)
	mustRegister(reg, "between", rules.BetweenRule)

	// Comparison rules from rules package
	mustRegister(reg, "same", rules.SameRule)
	mustRegister(reg, "different", rules.DifferentRule)
	mustRegister(reg, "in", rules.InRule)
	mustRegister(reg, "not_in", rules.NotInRule)
	mustRegister(reg, "confirmed", rules.ConfirmedRule)
	mustRegister(reg, "accepted", rules.AcceptedRule)

	// Conditional rules from rules package
	mustRegister(reg, "required_if", rules.RequiredIfRule)
	mustRegister(reg, "required_unless", rules.RequiredUnlessRule)
	mustRegister(reg, "required_with", rules.RequiredWithRule)
	mustRegister(reg, "required_without", rules.RequiredWithoutRule)

	// Date and time rules
	mustRegister(reg, "date", rules.DateRule)
	mustRegister(reg, "date_format", rules.DateFormatRule)
	mustRegister(reg, "timezone", rules.TimezoneRule)

	// Network rules
	mustRegister(reg, "ip", rules.IPRule)
	mustRegister(reg, "ipv4", rules.IPv4Rule)
	mustRegister(reg, "ipv6", rules.IPv6Rule)

	// Format rules
	mustRegister(reg, "regex", rules.RegexRule)
	mustRegister(reg, "json", rules.JSONRule)
	mustRegister(reg, "uuid", rules.UUIDRule)
	mustRegister(reg, "ulid", rules.ULIDRule)

	// String prefix/suffix/password rules
	mustRegister(reg, "starts_with", rules.StartsWithRule)
	mustRegister(reg, "ends_with", rules.EndsWithRule)
	mustRegister(reg, "password", rules.PasswordRule)

	// Numeric comparison rules
	mustRegister(reg, "gt", rules.GtRule)
	mustRegister(reg, "gte", rules.GteRule)
	mustRegister(reg, "lt", rules.LtRule)
	mustRegister(reg, "lte", rules.LteRule)

	// File rules (require values to be *multipart.FileHeader or equivalent).
	mustRegister(reg, "file", rules.FileRule)
	mustRegister(reg, "mimes", rules.MimesRule)
	mustRegister(reg, "image", rules.ImageRule)
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

// nullableRule marks a field as nullable. The rule itself is a pure
// pass-through (it never reports an error), so an explicit
// ValidateValue(v, Nullable()) call always resolves a handler. Its real
// effect lives in the engine loop: when a field carries "nullable" and its
// value is empty, the engine short-circuits and skips every other rule for
// that field.
//
// Emptiness here is deliberately narrow: nil or the empty string "". This is
// the HTML-form case that motivates the rule - an untouched optional text
// input submits "", which would otherwise fail rules like "url" or "email".
// It intentionally does NOT adopt requiredRule's broader empty-slice / map /
// empty-collection notion of emptiness.
//
// The short-circuit overrides ALL other rules for the field, INCLUDING
// "required". Combining Required() with Nullable() is contradictory; nullable
// wins, so such a field with an empty value passes.
func nullableRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	// Pass-through: the skip-when-empty behavior is implemented in the engine.
	return nil
}

// isNullableEmpty reports whether value counts as empty for the purposes of
// the "nullable" short-circuit: nil or the empty string "". Deliberately
// narrower than requiredRule's emptiness (no empty-slice/map handling) - see
// nullableRule.
func isNullableEmpty(value interface{}) bool {
	if value == nil {
		return true
	}
	s, ok := value.(string)
	return ok && s == ""
}

// rulesContainNullable reports whether the rule list includes the "nullable"
// rule.
func rulesContainNullable(rules []parsedRule) bool {
	for _, r := range rules {
		if r.name == "nullable" {
			return true
		}
	}
	return false
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

// presentRule validates that a field is present (but may be empty). Presence
// is resolved through fieldExists, which traverses dotted paths
// ("address.city") the same way the engine resolves field values, and treats
// a key present with a nil value as present.
func presentRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if data != nil && !fieldExists(data, field) {
		return fmt.Errorf("The %s field must be present.", field)
	}
	return nil
}
