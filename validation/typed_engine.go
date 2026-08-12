package validation

import (
	"fmt"

	"github.com/velocitykode/velocity/contract"
)

// validateNormalized runs an already-normalized rule set against data. It is
// the typed counterpart of Validate: same engine loop, same nullable
// short-circuit, same first-failure-per-field behaviour, but no rule string
// is parsed on the way in.
//
// Handlers carried by custom rules are NOT registered here; a validator that
// runs a set with carried handlers must register them first (see
// runNormalized).
func (v *defaultValidator) validateNormalized(data interface{}, rs normalizedRuleSet) (*ValidatedData, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	validated := contract.NewValidatedData()

	dataMap, err := toMap(data)
	if err != nil {
		return nil, fmt.Errorf("failed to convert data to map: %w", err)
	}

	for field, fieldRules := range rs.fields {
		v.validateFieldRules(validated, dataMap, field, fieldRules)
	}

	if validated.HasErrors() {
		return validated, validated.Errors()
	}

	return validated, nil
}

// runNormalized validates data against a normalized rule set on a fresh
// validator, registering the extra handlers first and then the handlers the
// set carries with it. This is the path that makes custom rules work without
// prior registration on a long-lived validator.
//
// extra maps rule name -> handler; subpackages pass DB-backed handlers
// (unique, exists) here so the orm dependency stays out of this package.
//
// The returned error reports a rule set that cannot be run at all (a custom
// handler colliding with a rule already registered on the validator), which
// is distinct from field-level validation failures carried by *Result.
func runNormalized(data map[string]interface{}, rs normalizedRuleSet, extra map[string]RuleHandler, messages ...Messages) (*Result, error) {
	v := newDefaultValidator()

	for name, handler := range extra {
		if err := v.registry.register(name, handler); err != nil {
			return nil, err
		}
	}
	for name, handler := range rs.custom {
		if err := v.registry.register(name, handler); err != nil {
			return nil, fmt.Errorf("%w: custom rule %q cannot be registered: %s", ErrInvalidRule, name, err)
		}
	}

	if len(messages) > 0 {
		v.SetMessages(messages[0])
	}

	result := &Result{input: data}

	_, err := v.validateNormalized(data, rs)
	if err != nil {
		if ve, ok := err.(ValidationErrors); ok {
			result.errors = ve.Errors
		}
	}

	return result, nil
}
