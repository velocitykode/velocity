package validation

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/contract"
)

// ErrInvalidRule is the sentinel wrapped by every rule-set normalization
// failure: a nil rule, an unnamed rule, an unusable Unique().Except() value,
// or a custom rule that cannot be registered. Normalization is fallible by
// design; it runs on adopter input at request time and must never panic.
var ErrInvalidRule = errors.New("velocity/validation: invalid rule")

// normalizedRuleSet is the engine-ready form of a rule set: rules already
// resolved to the internal parsedRule representation, plus the handlers
// carried by any custom rules in the set.
//
// Conversion is direct. No rule is ever rendered to a token string, so
// parameters carrying ',' or '|' survive intact.
type normalizedRuleSet struct {
	fields map[string][]parsedRule
	custom map[string]carriedRule
}

// carriedRule is a handler supplied by the rule set itself, kept with the
// rule value that supplied it so a second rule claiming the same name can be
// told apart from the same rule value reused across fields.
type carriedRule struct {
	handler RuleHandler
	source  contract.ValidationRule
}

// handlerCarrier is implemented by rule values that are expected to supply
// their own handler, so normalization can distinguish a custom rule missing
// its handler from a built-in rule, which legitimately has none.
type handlerCarrier interface {
	carriesHandler()
}

// selfValidatingRule is implemented by rule values that can only report a
// malformed argument at normalization time, e.g. Unique().Except() with a
// value that has no query-parameter representation.
type selfValidatingRule interface {
	validateRule() error
}

// builtinRuleNames is the set of rule names every validator resolves,
// derived from the rule table itself so the two cannot drift.
var builtinRuleNames = func() map[string]struct{} {
	names := make(map[string]struct{}, len(builtinRules))
	for name := range builtinRules {
		names[name] = struct{}{}
	}
	return names
}()

// dbRuleNames are the rule names supplied by validation/dbrules at
// evaluation time. They are reserved alongside the built-ins so a custom
// rule that would shadow them is reported during normalization instead of
// colliding when the DB handlers are installed.
var dbRuleNames = map[string]struct{}{
	"unique": {},
	"exists": {},
}

// isReservedRuleName reports whether name belongs to the framework.
func isReservedRuleName(name string) bool {
	if _, ok := builtinRuleNames[name]; ok {
		return true
	}
	_, ok := dbRuleNames[name]
	return ok
}

// normalizeRuleSet converts a rule set into its engine-ready form.
//
// It reports: a nil or typed-nil rule value, a rule with an empty name, an
// unusable Unique().Except() argument, a custom rule with a nil handler, a
// custom rule whose name shadows a framework rule, and two custom rules
// that share a name but not a handler.
func normalizeRuleSet(rs contract.ValidationRuleSet) (normalizedRuleSet, error) {
	out := normalizedRuleSet{}
	if len(rs) == 0 {
		return out, nil
	}
	out.fields = make(map[string][]parsedRule, len(rs))

	// One backing array for every field's rules: normalization runs per
	// request, so the field count must not drive the allocation count.
	total := 0
	for _, fieldRules := range rs {
		total += len(fieldRules)
	}
	buf := make([]parsedRule, 0, total)

	for field, fieldRules := range rs {
		if len(fieldRules) == 0 {
			out.fields[field] = nil
			continue
		}
		start := len(buf)
		for i, r := range fieldRules {
			if isNilRule(r) {
				return normalizedRuleSet{}, fmt.Errorf("%w: field %q rule %d is nil", ErrInvalidRule, field, i)
			}
			if sv, ok := r.(selfValidatingRule); ok {
				if err := sv.validateRule(); err != nil {
					return normalizedRuleSet{}, fmt.Errorf("field %q rule %d: %w", field, i, err)
				}
			}

			spec := r.Rule()
			if spec.Name == "" {
				return normalizedRuleSet{}, fmt.Errorf("%w: field %q rule %d has an empty name", ErrInvalidRule, field, i)
			}
			if _, carries := r.(handlerCarrier); carries && spec.Handler == nil {
				return normalizedRuleSet{}, fmt.Errorf("%w: custom rule %q on field %q has a nil handler", ErrInvalidRule, spec.Name, field)
			}
			if spec.Handler != nil {
				if err := out.carry(field, r, spec); err != nil {
					return normalizedRuleSet{}, err
				}
			}

			buf = append(buf, parsedRule{name: spec.Name, params: spec.Params})
		}
		// Capped slice: a consumer appending to one field's rules must not
		// write into the next field's entries.
		out.fields[field] = buf[start:len(buf):len(buf)]
	}

	return out, nil
}

// carry records a self-carrying handler so the engine can resolve the rule.
//
// One name maps to one handler per rule set. Reusing a single rule value
// across fields is legal; two distinct rule values claiming the same name are
// rejected, because nothing in a map traversal decides which handler wins.
// Go cannot compare functions, so the test is rule-value identity, not
// handler identity.
func (n *normalizedRuleSet) carry(field string, r contract.ValidationRule, spec contract.ValidationRuleSpec) error {
	if isReservedRuleName(spec.Name) {
		return fmt.Errorf("%w: custom rule %q on field %q shadows a built-in rule", ErrInvalidRule, spec.Name, field)
	}
	if n.custom == nil {
		n.custom = make(map[string]carriedRule, 1)
	}
	if existing, ok := n.custom[spec.Name]; ok {
		if !sameRuleValue(existing.source, r) {
			return fmt.Errorf("%w: custom rule %q is declared by two different rule values", ErrInvalidRule, spec.Name)
		}
		return nil
	}
	n.custom[spec.Name] = carriedRule{handler: spec.Handler, source: r}
	return nil
}

// sameRuleValue reports whether two rule values are the same instance.
// Custom returns a pointer, so reuse of one value compares equal while two
// separately built rules never do. A dynamic type that Go cannot compare
// (a struct holding a slice or a func) reports false rather than panicking
// on ==: an adopter rule value that cannot prove its identity is treated as
// a distinct declaration, which errors instead of silently picking one.
func sameRuleValue(a, b contract.ValidationRule) bool {
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Type() != bv.Type() || !av.Comparable() {
		return false
	}
	return a == b
}

// isNilRule reports whether r is nil or a typed nil, which would panic on
// the Rule() call.
func isNilRule(r contract.ValidationRule) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return v.IsNil()
	default:
		return false
	}
}
