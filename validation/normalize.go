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
	custom map[string]RuleHandler
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

// builtinRuleNames is the set of rule names the engine registers on every
// validator, derived from the registration function itself so the two
// cannot drift.
var builtinRuleNames = func() map[string]struct{} {
	reg := &ruleRegistry{rules: make(map[string]RuleHandler)}
	registerBuiltInRules(reg)
	names := make(map[string]struct{}, len(reg.rules))
	for name := range reg.rules {
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
				if err := out.carry(field, spec); err != nil {
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

// carry records a self-carrying handler for later registration on the
// validator that runs the set.
func (n *normalizedRuleSet) carry(field string, spec contract.ValidationRuleSpec) error {
	if isReservedRuleName(spec.Name) {
		return fmt.Errorf("%w: custom rule %q on field %q shadows a built-in rule", ErrInvalidRule, spec.Name, field)
	}
	if n.custom == nil {
		n.custom = make(map[string]RuleHandler, 1)
	}
	if existing, ok := n.custom[spec.Name]; ok {
		if !sameHandler(existing, spec.Handler) {
			return fmt.Errorf("%w: custom rule %q is declared with two different handlers", ErrInvalidRule, spec.Name)
		}
		return nil
	}
	n.custom[spec.Name] = spec.Handler
	return nil
}

// sameHandler reports whether two handlers refer to the same function.
// Go cannot compare funcs, so identity is the code pointer: two closures
// created from one function literal compare equal even when they captured
// different variables. Treating those as the same rule is the conservative
// outcome, it keeps a legitimate repeated rule from being rejected.
func sameHandler(a, b RuleHandler) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
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
