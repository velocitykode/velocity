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

// builtinRules is the rule table every validator resolves against. It is a
// package-level literal so a duplicate name is a compile error, not a runtime
// failure mode, and it is never written after initialization, so validators
// share it without synchronization.
var builtinRules = map[string]RuleHandler{
	// Presence rules
	"required": requiredRule,
	"nullable": nullableRule,
	"filled":   filledRule,
	"present":  presentRule,

	// Type rules from rules package
	"string":  rules.StringRule,
	"integer": rules.IntegerRule,
	"numeric": rules.NumericRule,
	"boolean": rules.BooleanRule,
	"array":   rules.ArrayRule,

	// String rules from rules package
	"email":      rules.EmailRule,
	"url":        rules.URLRule,
	"url_public": rules.URLPublicRule,
	"alpha":      rules.AlphaRule,
	"alpha_dash": rules.AlphaDashRule,
	"alpha_num":  rules.AlphaNumRule,

	// Size rules from rules package
	"min":     rules.MinRule,
	"max":     rules.MaxRule,
	"size":    rules.SizeRule,
	"between": rules.BetweenRule,

	// Comparison rules from rules package
	"same":      rules.SameRule,
	"different": rules.DifferentRule,
	"in":        rules.InRule,
	"not_in":    rules.NotInRule,
	"confirmed": rules.ConfirmedRule,
	"accepted":  rules.AcceptedRule,

	// Conditional rules from rules package
	"required_if":      rules.RequiredIfRule,
	"required_unless":  rules.RequiredUnlessRule,
	"required_with":    rules.RequiredWithRule,
	"required_without": rules.RequiredWithoutRule,

	// Date and time rules
	"date":        rules.DateRule,
	"date_format": rules.DateFormatRule,
	"timezone":    rules.TimezoneRule,

	// Network rules
	"ip":   rules.IPRule,
	"ipv4": rules.IPv4Rule,
	"ipv6": rules.IPv6Rule,

	// Format rules
	"regex": rules.RegexRule,
	"json":  rules.JSONRule,
	"uuid":  rules.UUIDRule,
	"ulid":  rules.ULIDRule,

	// String prefix/suffix/password rules
	"starts_with": rules.StartsWithRule,
	"ends_with":   rules.EndsWithRule,
	"password":    rules.PasswordRule,

	// Numeric comparison rules
	"gt":  rules.GtRule,
	"gte": rules.GteRule,
	"lt":  rules.LtRule,
	"lte": rules.LteRule,

	// File rules (require values to be *multipart.FileHeader or equivalent).
	"file":  rules.FileRule,
	"mimes": rules.MimesRule,
	"image": rules.ImageRule,
}

// ruleRegistry holds the handlers installed on one validator beyond the
// built-in table: the DB-backed handlers a Check helper supplies for the
// duration of one run. Rules an adopter writes carry their own handler
// (see Custom), so nothing outside this package registers into it.
//
// Lookups fall back to builtinRules, which is immutable, so construction
// copies nothing. The per-validator map is mutex-guarded because
// app.Services.Validator is a long-lived singleton shared across every
// handler goroutine.
type ruleRegistry struct {
	mu    sync.RWMutex
	rules map[string]RuleHandler
}

// register reports a nil handler, a name already taken on this validator, or
// a name that shadows a built-in. It runs on caller-supplied handlers (the
// Check helpers' extra map), so it reports rather than panics.
func (r *ruleRegistry) register(name string, handler RuleHandler) error {
	if handler == nil {
		return contract.NewRegistrationError("validation", fmt.Sprintf("nil handler for rule %q", name))
	}
	if _, builtin := builtinRules[name]; builtin {
		return contract.NewRegistrationError("validation", fmt.Sprintf("rule %q already registered", name))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.rules[name]; exists {
		return contract.NewRegistrationError("validation", fmt.Sprintf("rule %q already registered", name))
	}
	r.rules[name] = handler
	return nil
}

// get retrieves a validation rule handler, preferring the handlers installed
// on this validator over the built-in table.
func (r *ruleRegistry) get(name string) (RuleHandler, bool) {
	r.mu.RLock()
	handler, exists := r.rules[name]
	r.mu.RUnlock()
	if exists {
		return handler, true
	}
	handler, exists = builtinRules[name]
	return handler, exists
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
