package rules

import (
	"fmt"
	"strconv"
)

// IntegerRule validates that a value is an integer
func IntegerRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	switch v := value.(type) {
	case int, int8, int16, int32, int64:
		return nil
	case uint, uint8, uint16, uint32, uint64:
		return nil
	case float32, float64:
		// Accept whole number floats
		if v == float64(int64(v.(float64))) {
			return nil
		}
		return fmt.Errorf("The %s field must be an integer.", field)
	case string:
		// Try to parse string as integer
		if v == "" {
			return fmt.Errorf("The %s field must be an integer.", field)
		}
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return fmt.Errorf("The %s field must be an integer.", field)
		}
		return nil
	default:
		return fmt.Errorf("The %s field must be an integer.", field)
	}
}

// NumericRule validates that a value is numeric
func NumericRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	switch v := value.(type) {
	case int, int8, int16, int32, int64:
		return nil
	case uint, uint8, uint16, uint32, uint64:
		return nil
	case float32, float64:
		return nil
	case string:
		// Try to parse string as number
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return fmt.Errorf("The %s field must be numeric.", field)
		}
		return nil
	default:
		return fmt.Errorf("The %s field must be numeric.", field)
	}
}

// BooleanRule validates that a value is a boolean
func BooleanRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	switch v := value.(type) {
	case bool:
		return nil
	case string:
		// Accept string representations of boolean
		if v == "true" || v == "false" || v == "1" || v == "0" || v == "yes" || v == "no" {
			return nil
		}
		return fmt.Errorf("The %s field must be a boolean.", field)
	case int:
		if v == 0 || v == 1 {
			return nil
		}
		return fmt.Errorf("The %s field must be a boolean.", field)
	default:
		return fmt.Errorf("The %s field must be a boolean.", field)
	}
}

// ArrayRule validates that a value is an array
func ArrayRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	switch value.(type) {
	case []interface{}, []string, []int, []float64:
		return nil
	default:
		return fmt.Errorf("The %s field must be an array.", field)
	}
}
