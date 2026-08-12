package testing

import (
	"testing"

	"github.com/velocitykode/velocity/validation"
)

func TestNewTestValidator(t *testing.T) {
	v := NewTestValidator()
	if v == nil {
		t.Fatal("NewTestValidator returned nil")
	}
}

func TestRuleAssertion(t *testing.T) {
	// Passing input with a valid email should not fail.
	RuleAssertion(t, map[string]interface{}{"email": "a@b.co"}, false, validation.Required(), validation.Email())

	// Invalid email should fail (expectedErr=true).
	RuleAssertion(t, map[string]interface{}{"email": "not-an-email"}, true, validation.Required(), validation.Email())

	// Required field present, min length met.
	RuleAssertion(t, map[string]interface{}{"name": "Alice"}, false, validation.Required(), validation.Min(3))

	// Required field missing (empty string).
	RuleAssertion(t, map[string]interface{}{"name": ""}, true, validation.Required(), validation.Min(3))
}
