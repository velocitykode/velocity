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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/velocitykode/velocity/contract"
)

// Validator provides validation functionality. The canonical declaration
// lives in the stdlib-only contract leaf; this alias keeps the validation
// API byte-identical for existing callers.
type Validator = contract.Validator

// Rules defines validation rules per field. Rules is the canonical adopter
// facing type and matches the shape returned by vform.FormRequest.Rules():
// each field maps to a slice of individual rule strings. Canonical
// declaration lives in the contract leaf as ValidationRules.
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
type Rules = contract.ValidationRules

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

// Messages defines custom error messages. Canonical declaration lives in the
// contract leaf as ValidationMessages.
type Messages = contract.ValidationMessages

// ValidatedData contains validated and cleaned data. Canonical declaration
// (struct and methods) lives in the stdlib-only contract leaf.
type ValidatedData = contract.ValidatedData

// defaultValidator is the default validator implementation
type defaultValidator struct {
	registry *RuleRegistry
	messages Messages
	mu       sync.RWMutex
}

// defaultValidator must satisfy the contract Validator interface.
var _ contract.Validator = (*defaultValidator)(nil)

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

	validated := contract.NewValidatedData()

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

		// "nullable" short-circuit: a field carrying "nullable" whose value is
		// empty (nil or "") skips ALL other rules - including "required" - and
		// is recorded as validated. See nullableRule for the semantics.
		if rulesContainNullable(fieldRules) && isNullableEmpty(value) {
			validated.Set(field, value)
			continue
		}

		for _, rule := range fieldRules {
			if err := v.validateField(field, value, rule, dataMap); err != nil {
				validated.AddError(field, err.Error(), rule.name)
				break // Stop on first error for this field
			}
		}

		// Add to validated data if no errors
		if !validated.Errors().HasError(field) {
			validated.Set(field, value)
		}
	}

	if validated.HasErrors() {
		return validated, validated.Errors()
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

	// Mirror the engine's "nullable" short-circuit: an empty value (nil or "")
	// with "nullable" skips every other rule. See nullableRule.
	if rulesContainNullable(fieldRules) && isNullableEmpty(value) {
		return nil
	}

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

	// rule.params may be backed by a slice shared from the parse cache (see
	// ruleCache). Hand the handler its own copy so a custom rule that mutates
	// params in place cannot corrupt cached entries or race other goroutines
	// validating the same rule set. No-param rules pay nothing.
	params := rule.params
	if len(params) > 0 {
		params = append([]string(nil), params...)
	}

	if err := handler(field, value, params, data); err != nil {
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

// ruleCache memoizes parsed rule slices keyed by their token list. Rule sets
// are authored by application code (FormRequest.Rules() methods, inline
// Validate calls), not by request input, so the key space is bounded by the
// app's distinct rule definitions. Without the cache, parseRuleSlice re-split
// and re-allocated an identical parsedRule slice on every Validate call
// (~65% of Validate's allocations). Cached entries are shared across calls and
// goroutines, so they must be treated as immutable: validateField hands each
// handler a defensive copy of rule.params (a custom rule may mutate params in
// place), and nothing else writes through a cached slice. ruleCacheCap bounds
// growth defensively; past the cap, parsing still happens, it just is not memoized.
var (
	ruleCache     sync.Map // map[string][]parsedRule
	ruleCacheSize atomic.Int64
)

const ruleCacheCap = 4096

// parseRuleSlice parses an ordered slice of rule tokens into parsedRule
// values. Each token may itself contain '|' (legacy pipe-string form) and
// is re-split before parsing. Empty / whitespace-only tokens are dropped.
// Results are memoized in ruleCache keyed by the token list.
func parseRuleSlice(tokens []string) []parsedRule {
	if len(tokens) == 0 {
		return nil
	}
	key := ruleCacheKey(tokens)
	if cached, ok := ruleCache.Load(key); ok {
		return cached.([]parsedRule)
	}
	rules := parseRuleSliceUncached(tokens)
	if ruleCacheSize.Load() < ruleCacheCap {
		if _, loaded := ruleCache.LoadOrStore(key, rules); !loaded {
			ruleCacheSize.Add(1)
		}
	}
	return rules
}

// ruleCacheKey builds a collision-free key for a token list by joining tokens
// with a NUL separator (never present in rule strings). The single-token case
// returns the token directly, avoiding an allocation.
func ruleCacheKey(tokens []string) string {
	if len(tokens) == 1 {
		return tokens[0]
	}
	n := len(tokens) - 1
	for _, t := range tokens {
		n += len(t)
	}
	var b strings.Builder
	b.Grow(n)
	for i, t := range tokens {
		if i > 0 {
			b.WriteByte(0)
		}
		b.WriteString(t)
	}
	return b.String()
}

// parseRuleSliceUncached is the raw parser behind parseRuleSlice's cache.
func parseRuleSliceUncached(tokens []string) []parsedRule {
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
