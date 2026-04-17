package testing

import "testing"

func TestNewTestValidator(t *testing.T) {
	v := NewTestValidator()
	if v == nil {
		t.Fatal("NewTestValidator returned nil")
	}
}

func TestRuleAssertion(t *testing.T) {
	// Passing input with a valid email should not fail.
	RuleAssertion(t, "required|email", map[string]interface{}{"email": "a@b.co"}, false)

	// Invalid email should fail (expectedErr=true).
	RuleAssertion(t, "required|email", map[string]interface{}{"email": "not-an-email"}, true)

	// Required field present, min length met.
	RuleAssertion(t, "required|min:3", map[string]interface{}{"name": "Alice"}, false)

	// Required field missing (empty string).
	RuleAssertion(t, "required|min:3", map[string]interface{}{"name": ""}, true)
}
