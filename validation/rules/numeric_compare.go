package rules

import (
	"fmt"
	"strconv"
)

// GtRule validates that a numeric value is strictly greater than a threshold
// (numeric literal) or another field's numeric value.
//
// Usage:
//
//	gt:10           — value > 10
//	gt:other_field  — value > other_field's numeric value
func GtRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	return compareNumeric(field, value, params, data, "gt", func(a, b float64) bool { return a > b }, "greater than")
}

// GteRule validates that a numeric value is greater than or equal to a
// threshold or another field's numeric value.
func GteRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	return compareNumeric(field, value, params, data, "gte", func(a, b float64) bool { return a >= b }, "greater than or equal to")
}

// LtRule validates that a numeric value is strictly less than a threshold.
func LtRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	return compareNumeric(field, value, params, data, "lt", func(a, b float64) bool { return a < b }, "less than")
}

// LteRule validates that a numeric value is less than or equal to a threshold.
func LteRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	return compareNumeric(field, value, params, data, "lte", func(a, b float64) bool { return a <= b }, "less than or equal to")
}

func compareNumeric(field string, value interface{}, params []string, data map[string]interface{}, ruleName string, cmp func(a, b float64) bool, phrase string) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 || params[0] == "" {
		return fmt.Errorf("The %s rule requires 1 parameter.", ruleName)
	}

	av, ok := toFloat(value)
	if !ok {
		return fmt.Errorf("The %s field must be numeric.", field)
	}

	threshold, ok := resolveThreshold(params[0], data)
	if !ok {
		return fmt.Errorf("The %s rule parameter %q must be numeric or an existing numeric field.", ruleName, params[0])
	}

	if !cmp(av, threshold) {
		return fmt.Errorf("The %s field must be %s %v.", field, phrase, formatFloat(threshold))
	}
	return nil
}

func resolveThreshold(raw string, data map[string]interface{}) (float64, bool) {
	if f, err := strconv.ParseFloat(raw, 64); err == nil {
		return f, true
	}
	if data != nil {
		if other, exists := data[raw]; exists {
			return toFloat(other)
		}
	}
	return 0, false
}

func toFloat(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	case string:
		if f, err := strconv.ParseFloat(x, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// formatFloat prints integer floats without a trailing .0.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
