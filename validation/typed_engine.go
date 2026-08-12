package validation

import (
	"fmt"

	"github.com/velocitykode/velocity/contract"
)

// validateNormalized runs an already-normalized rule set against data.
// Handlers the set carries are passed down as an overlay rather than
// registered, so a custom rule works on a shared long-lived validator
// without mutating it.
func (v *defaultValidator) validateNormalized(data interface{}, rs normalizedRuleSet) (*ValidatedData, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// Every handler this set needs is installed by now: the built-ins, the
	// extras a Check helper supplied, and the ones the rules carry. A name
	// that still does not resolve is a configuration bug, so it is reported
	// here rather than evaluated into a field error.
	if err := v.checkResolvable(rs); err != nil {
		return nil, err
	}

	validated := contract.NewValidatedData()

	dataMap, err := toMap(data)
	if err != nil {
		return nil, fmt.Errorf("failed to convert data to map: %w", err)
	}

	for field, fieldRules := range rs.fields {
		v.validateFieldRules(validated, dataMap, field, fieldRules, rs.custom)
	}

	if validated.HasErrors() {
		return validated, validated.Errors()
	}

	return validated, nil
}

// runNormalized validates data against a normalized rule set on a fresh
// validator holding the extra handlers.
//
// extra maps rule name -> handler; subpackages pass DB-backed handlers
// (unique, exists) here so the orm dependency stays out of this package.
//
// The returned error reports a rule set that cannot be run at all (an extra
// handler that cannot be registered, a carried handler that would shadow one,
// or a rule name nothing resolves), which is distinct from field-level
// validation failures carried by *Result.
func runNormalized(data map[string]interface{}, rs normalizedRuleSet, extra map[string]RuleHandler, messages ...Messages) (*Result, error) {
	v := newDefaultValidator()

	for name, handler := range extra {
		if err := v.registry.register(name, handler); err != nil {
			return nil, err
		}
		if _, carried := rs.custom[name]; carried {
			return nil, fmt.Errorf("%w: custom rule %q shadows the %q rule supplied by the caller", ErrInvalidRule, name, name)
		}
	}

	if len(messages) > 0 {
		v.SetMessages(messages[0])
	}

	result := &Result{input: data}

	_, err := v.validateNormalized(data, rs)
	if err != nil {
		ve, ok := err.(ValidationErrors)
		if !ok {
			// Not a field-level failure: the set could not run at all.
			return nil, err
		}
		result.errors = ve.Errors
	}

	return result, nil
}
