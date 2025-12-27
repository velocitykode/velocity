package rules

import (
	"fmt"
	"regexp"
	"strconv"
)

// StringRule validates that a value is a string
func StringRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil // Let required rule handle nil
	}

	if _, ok := value.(string); !ok {
		return fmt.Errorf("%s must be a string", field)
	}
	return nil
}

// EmailRule validates that a value is a valid email
func EmailRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}

	// Basic email regex
	emailRegex := regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)
	if !emailRegex.MatchString(str) {
		return fmt.Errorf("%s must be a valid email address", field)
	}
	return nil
}

// URLRule validates that a value is a valid URL
func URLRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}

	// Basic URL regex
	urlRegex := regexp.MustCompile(`^https?://[^\s]+$`)
	if !urlRegex.MatchString(str) {
		return fmt.Errorf("%s must be a valid URL", field)
	}
	return nil
}

// AlphaRule validates that a value contains only alphabetic characters
func AlphaRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}

	alphaRegex := regexp.MustCompile(`^[a-zA-Z]+$`)
	if !alphaRegex.MatchString(str) {
		return fmt.Errorf("%s must contain only alphabetic characters", field)
	}
	return nil
}

// AlphaDashRule validates that a value contains only alpha-numeric characters, dashes, and underscores
func AlphaDashRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}

	alphaDashRegex := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !alphaDashRegex.MatchString(str) {
		return fmt.Errorf("%s must contain only letters, numbers, dashes, and underscores", field)
	}
	return nil
}

// AlphaNumRule validates that a value contains only alpha-numeric characters
func AlphaNumRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", field)
	}

	alphaNumRegex := regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	if !alphaNumRegex.MatchString(str) {
		return fmt.Errorf("%s must contain only letters and numbers", field)
	}
	return nil
}

// MinRule validates minimum length/size
func MinRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("min rule requires 1 parameter")
	}

	min, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("min parameter must be a number")
	}

	switch v := value.(type) {
	case string:
		if len(v) < min {
			return fmt.Errorf("%s must be at least %d characters", field, min)
		}
	case int:
		if v < min {
			return fmt.Errorf("%s must be at least %d", field, min)
		}
	case float64:
		if v < float64(min) {
			return fmt.Errorf("%s must be at least %d", field, min)
		}
	case []interface{}:
		if len(v) < min {
			return fmt.Errorf("%s must have at least %d items", field, min)
		}
	case []string:
		if len(v) < min {
			return fmt.Errorf("%s must have at least %d items", field, min)
		}
	default:
		return fmt.Errorf("%s type not supported for min rule", field)
	}

	return nil
}

// MaxRule validates maximum length/size
func MaxRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("max rule requires 1 parameter")
	}

	max, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("max parameter must be a number")
	}

	switch v := value.(type) {
	case string:
		if len(v) > max {
			return fmt.Errorf("%s must not exceed %d characters", field, max)
		}
	case int:
		if v > max {
			return fmt.Errorf("%s must not exceed %d", field, max)
		}
	case float64:
		if v > float64(max) {
			return fmt.Errorf("%s must not exceed %d", field, max)
		}
	case []interface{}:
		if len(v) > max {
			return fmt.Errorf("%s must not have more than %d items", field, max)
		}
	case []string:
		if len(v) > max {
			return fmt.Errorf("%s must not have more than %d items", field, max)
		}
	default:
		return fmt.Errorf("%s type not supported for max rule", field)
	}

	return nil
}

// SizeRule validates exact size
func SizeRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("size rule requires 1 parameter")
	}

	size, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("size parameter must be a number")
	}

	switch v := value.(type) {
	case string:
		if len(v) != size {
			return fmt.Errorf("%s must be exactly %d characters", field, size)
		}
	case int:
		if v != size {
			return fmt.Errorf("%s must be exactly %d", field, size)
		}
	case []interface{}:
		if len(v) != size {
			return fmt.Errorf("%s must have exactly %d items", field, size)
		}
	case []string:
		if len(v) != size {
			return fmt.Errorf("%s must have exactly %d items", field, size)
		}
	default:
		return fmt.Errorf("%s type not supported for size rule", field)
	}

	return nil
}

// BetweenRule validates that a value is between min and max
func BetweenRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 2 {
		return fmt.Errorf("between rule requires 2 parameters")
	}

	min, err1 := strconv.Atoi(params[0])
	max, err2 := strconv.Atoi(params[1])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("between parameters must be numbers")
	}

	switch v := value.(type) {
	case string:
		// Try to convert string to int for numeric validation
		if intVal, err := strconv.Atoi(v); err == nil {
			if intVal < min || intVal > max {
				return fmt.Errorf("%s must be between %d and %d", field, min, max)
			}
		} else {
			// If not a number, check string length
			length := len(v)
			if length < min || length > max {
				return fmt.Errorf("%s must be between %d and %d characters", field, min, max)
			}
		}
	case int:
		if v < min || v > max {
			return fmt.Errorf("%s must be between %d and %d", field, min, max)
		}
	case float64:
		if v < float64(min) || v > float64(max) {
			return fmt.Errorf("%s must be between %d and %d", field, min, max)
		}
	case []interface{}:
		length := len(v)
		if length < min || length > max {
			return fmt.Errorf("%s must have between %d and %d items", field, min, max)
		}
	default:
		return fmt.Errorf("%s type not supported for between rule", field)
	}

	return nil
}
