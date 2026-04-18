// Package testing provides helpers for exercising validation rules in unit
// tests. It is a thin wrapper around the validation package; import it from
// your _test.go files, not from production code.
package testing

import (
	"errors"
	"strings"
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

// AssertErrorRule fails the test unless err is a validation.ValidationErrors
// that contains an error on field produced by the named rule.
//
// Prefer this over HasError(field) in rule-specific tests: HasError only
// tells you "something failed", so a test that expects "required" would still
// pass if "email" failed instead. AssertErrorRule catches that kind of
// mutation.
func AssertErrorRule(t testing.TB, err error, field, rule string) {
	t.Helper()
	var verr validation.ValidationErrors
	if !errors.As(err, &verr) {
		t.Fatalf("AssertErrorRule(%q, %q): expected validation.ValidationErrors, got %T: %v", field, rule, err, err)
	}
	if !verr.HasError(field) {
		t.Fatalf("AssertErrorRule(%q, %q): field has no errors; got %v", field, rule, verr.All())
	}
	if !verr.HasRule(field, rule) {
		t.Fatalf("AssertErrorRule(%q, %q): field failed rules %v, not %q", field, rule, verr.RulesFor(field), rule)
	}
}

// AssertErrorMessage fails the test unless the error on field contains the
// given substring. Use for rules that produce user-facing copy where the
// wording is load-bearing (payment flows, auth messaging) — otherwise prefer
// AssertErrorRule so tests don't break on copy edits.
func AssertErrorMessage(t testing.TB, err error, field, substring string) {
	t.Helper()
	var verr validation.ValidationErrors
	if !errors.As(err, &verr) {
		t.Fatalf("AssertErrorMessage(%q, %q): expected validation.ValidationErrors, got %T: %v", field, substring, err, err)
	}
	for _, msg := range verr.All()[field] {
		if strings.Contains(msg, substring) {
			return
		}
	}
	t.Fatalf("AssertErrorMessage(%q, %q): no message containing substring; got %v", field, substring, verr.All()[field])
}
