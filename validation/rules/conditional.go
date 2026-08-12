package rules

import "fmt"

// RequiredIfRule validates that a field is required when another field equals a given value.
// Usage: required_if:other_field,value
func RequiredIfRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if len(params) < 2 {
		return fmt.Errorf("The required_if rule requires at least 2 parameters.")
	}

	otherField := params[0]
	expectedValue := params[1]

	otherValue, exists := data[otherField]
	if !exists {
		return nil
	}

	if fmt.Sprintf("%v", otherValue) != expectedValue {
		return nil
	}

	// Condition met — field is required
	return checkRequired(field, value)
}

// RequiredUnlessRule validates that a field is required unless another field equals a given value.
// Usage: required_unless:other_field,value
func RequiredUnlessRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if len(params) < 2 {
		return fmt.Errorf("The required_unless rule requires at least 2 parameters.")
	}

	otherField := params[0]
	exemptValue := params[1]

	otherValue, exists := data[otherField]
	if exists && fmt.Sprintf("%v", otherValue) == exemptValue {
		return nil
	}

	// Condition met — field is required
	return checkRequired(field, value)
}

// RequiredWithRule validates that a field is required when ANY of the listed
// fields is present. Every parameter is honored.
// Usage: required_with:other_field or required_with:phone,mobile
func RequiredWithRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if len(params) < 1 {
		return fmt.Errorf("The required_with rule requires at least 1 parameter.")
	}

	for _, otherField := range params {
		if _, exists := data[otherField]; exists {
			// One of the listed fields is present, this field is required.
			return checkRequired(field, value)
		}
	}

	return nil
}

// RequiredWithoutRule validates that a field is required when ANY of the
// listed fields is absent. Every parameter is honored.
// Usage: required_without:other_field or required_without:phone,mobile
func RequiredWithoutRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if len(params) < 1 {
		return fmt.Errorf("The required_without rule requires at least 1 parameter.")
	}

	for _, otherField := range params {
		if _, exists := data[otherField]; !exists {
			// One of the listed fields is absent, this field is required.
			return checkRequired(field, value)
		}
	}

	return nil
}

// checkRequired checks that a value is present and not empty.
func checkRequired(field string, value interface{}) error {
	if value == nil {
		return fmt.Errorf("The %s field is required.", field)
	}

	switch v := value.(type) {
	case string:
		if v == "" {
			return fmt.Errorf("The %s field is required.", field)
		}
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	case []string:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	case map[string]interface{}:
		if len(v) == 0 {
			return fmt.Errorf("The %s field is required.", field)
		}
	}

	return nil
}
