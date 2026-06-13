package rules

import (
	"fmt"
	"reflect"
	"strings"
)

// SameRule validates that a field matches another field
func SameRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("The same rule requires 1 parameter.")
	}

	otherField := params[0]
	otherValue, exists := data[otherField]
	if !exists {
		return fmt.Errorf("The %s field must match %s.", field, otherField)
	}

	if !reflect.DeepEqual(value, otherValue) {
		return fmt.Errorf("The %s field must match %s.", field, otherField)
	}

	return nil
}

// DifferentRule validates that a field is different from another field
func DifferentRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("The different rule requires 1 parameter.")
	}

	otherField := params[0]
	otherValue, exists := data[otherField]
	if !exists {
		return nil // If other field doesn't exist, they're different
	}

	if reflect.DeepEqual(value, otherValue) {
		return fmt.Errorf("The %s field must be different from %s.", field, otherField)
	}

	return nil
}

// InRule validates that a value is in a list of allowed values
func InRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) == 0 {
		return fmt.Errorf("The in rule requires at least 1 parameter.")
	}

	valueStr := fmt.Sprintf("%v", value)
	for _, allowed := range params {
		if valueStr == allowed {
			return nil
		}
	}

	return fmt.Errorf("The selected %s is invalid.", field)
}

// NotInRule validates that a value is not in a list of values
func NotInRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) == 0 {
		return fmt.Errorf("The not_in rule requires at least 1 parameter.")
	}

	valueStr := fmt.Sprintf("%v", value)
	for _, disallowed := range params {
		if valueStr == disallowed {
			return fmt.Errorf("The selected %s is invalid.", field)
		}
	}

	return nil
}

// ConfirmedRule validates that a field has a matching sibling field named
// "<field>_confirmation". For example, applying `confirmed` to "password"
// requires "password_confirmation" to be present in the input data and to
// hold the same value (compared via reflect.DeepEqual).
//
// Error attribution: when the values do not match (or the confirmation
// field is missing), the returned error is attached by the validator to
// the ORIGINAL field key (e.g. "password"), NOT to the sibling
// "<field>_confirmation". Attributing the error to the primary field lets
// frontends bind validation messages to the input the user actually edits.
//
// In practice, a failing `confirmed` rule on "password" produces an
// errors map shaped like:
//
//	{ "password": ["The password confirmation does not match."] }
//
// The frontend should bind/display this message on the primary input
// (e.g. the "password" field), not on the "password_confirmation" input.
func ConfirmedRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	confirmField := field + "_confirmation"
	confirmValue, exists := data[confirmField]
	if !exists {
		return fmt.Errorf("The %s confirmation does not match.", field)
	}

	if !reflect.DeepEqual(value, confirmValue) {
		return fmt.Errorf("The %s confirmation does not match.", field)
	}

	return nil
}

// AcceptedRule validates that a field is accepted (yes, on, 1, true)
func AcceptedRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return fmt.Errorf("The %s field must be accepted.", field)
	}

	switch v := value.(type) {
	case bool:
		if !v {
			return fmt.Errorf("The %s field must be accepted.", field)
		}
	case string:
		accepted := []string{"yes", "on", "1", "true"}
		lower := strings.ToLower(v)
		for _, a := range accepted {
			if lower == a {
				return nil
			}
		}
		return fmt.Errorf("The %s field must be accepted.", field)
	case int:
		if v != 1 {
			return fmt.Errorf("The %s field must be accepted.", field)
		}
	default:
		return fmt.Errorf("The %s field must be accepted.", field)
	}

	return nil
}
