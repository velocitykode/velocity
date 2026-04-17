// Package testing provides helpers for exercising validation rules in unit
// tests. It is a thin wrapper around the validation package; import it from
// your _test.go files, not from production code.
package testing

import (
	"testing"

	"github.com/velocitykode/velocity/validation"
)

// NewTestValidator returns a fresh validation.Validator with all built-in
// rules registered. Call RegisterRule or SetMessages on the returned value
// to customise for a particular test.
func NewTestValidator() validation.Validator {
	return validation.NewValidator()
}

// RuleAssertion runs a single built-in rule against input and fails the
// test when the observed outcome (error or no error) disagrees with
// expected. expected is a pipe-separated rule string — the same format the
// validation package accepts — so callers can express composite rules like
// "required|min:3".
//
// Example:
//
//	RuleAssertion(t, "required|email", map[string]interface{}{"email": "bad"}, true)
//
// expectedErr=true means the input is expected to fail validation;
// expectedErr=false means it should pass.
func RuleAssertion(t *testing.T, rule string, input map[string]interface{}, expectedErr bool) {
	t.Helper()
	v := NewTestValidator()
	rules := validation.Rules{}
	// Apply the same rule to every key in the input so callers only need
	// to supply the rule string once.
	for k := range input {
		rules[k] = rule
	}
	_, err := v.Validate(input, rules)
	gotErr := err != nil
	if gotErr != expectedErr {
		t.Fatalf("RuleAssertion(%q, %+v): want err=%v, got err=%v (%v)",
			rule, input, expectedErr, gotErr, err)
	}
}
