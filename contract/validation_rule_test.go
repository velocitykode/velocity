package contract

import (
	"reflect"
	"testing"
)

// fakeRule is a minimal ValidationRule implementation.
type fakeRule struct {
	spec ValidationRuleSpec
}

func (f fakeRule) Rule() ValidationRuleSpec { return f.spec }

func TestValidationRuleSet_HoldsRules(t *testing.T) {
	handler := func(field string, value interface{}, params []string, data map[string]interface{}) error {
		return nil
	}

	set := ValidationRuleSet{
		"email": {
			fakeRule{spec: ValidationRuleSpec{Name: "required"}},
			fakeRule{spec: ValidationRuleSpec{Name: "unique", Params: []string{"users", "email"}}},
		},
		"score": {
			fakeRule{spec: ValidationRuleSpec{Name: "even", Handler: handler}},
		},
	}

	if len(set) != 2 {
		t.Fatalf("field count = %d, want 2", len(set))
	}

	got := set["email"][1].Rule()
	if got.Name != "unique" {
		t.Errorf("name = %q, want unique", got.Name)
	}
	if !reflect.DeepEqual(got.Params, []string{"users", "email"}) {
		t.Errorf("params = %#v, want [users email]", got.Params)
	}
	if got.Handler != nil {
		t.Error("built-in rule spec must not carry a handler")
	}
	if set["score"][0].Rule().Handler == nil {
		t.Error("custom rule spec must carry its handler")
	}
}

// TestValidationRuleSet_IsDefinedType pins the defined-type decision: a
// structurally identical map is not assignable without a conversion, so an
// adopter cannot drift off the type an interface is declared against.
func TestValidationRuleSet_IsDefinedType(t *testing.T) {
	if reflect.TypeOf(ValidationRuleSet{}).Name() != "ValidationRuleSet" {
		t.Error("ValidationRuleSet must be a defined type, not an alias")
	}
}

func TestValidationMessages_KeyedByFieldAndRule(t *testing.T) {
	messages := ValidationMessages{
		{Field: "email", Rule: "required"}: "We need your email.",
		{Field: "email", Rule: "unique"}:   "That email is taken.",
	}

	if got := messages[ValidationMessageKey{Field: "email", Rule: "unique"}]; got != "That email is taken." {
		t.Errorf("message = %q, want the unique override", got)
	}
	if _, ok := messages[ValidationMessageKey{Field: "email", Rule: "email"}]; ok {
		t.Error("unrelated key resolved to a message")
	}
	if len(messages) != 2 {
		t.Errorf("message count = %d, want 2", len(messages))
	}
}
