package rules

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// StartsWithRule validates that a string begins with one of the supplied
// prefixes. Usage: starts_with:foo or starts_with:http,https
func StartsWithRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 {
		return fmt.Errorf("The starts_with rule requires at least 1 parameter.")
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a string.", field)
	}
	for _, prefix := range params {
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(str, prefix) {
			return nil
		}
	}
	return fmt.Errorf("The %s field must start with one of the following: %s.", field, strings.Join(params, ", "))
}

// EndsWithRule validates that a string ends with one of the supplied
// suffixes. Usage: ends_with:.pdf or ends_with:.jpg,.png
func EndsWithRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 {
		return fmt.Errorf("The ends_with rule requires at least 1 parameter.")
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a string.", field)
	}
	for _, suffix := range params {
		if suffix == "" {
			continue
		}
		if strings.HasSuffix(str, suffix) {
			return nil
		}
	}
	return fmt.Errorf("The %s field must end with one of the following: %s.", field, strings.Join(params, ", "))
}

// PasswordRule validates that a value meets baseline password requirements.
//
// Defaults (no parameters): min length 8, one upper, one lower, one digit,
// one symbol. Parameters override individual floor values:
//
//	password:12         - min length 12 only
//	password:8,mixed,num,symbol
//
// Recognised flags: mixed (upper+lower), num, symbol, length:<n>.
func PasswordRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return fmt.Errorf("The %s field is required.", field)
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return fmt.Errorf("The %s field is required.", field)
	}

	minLen := 8
	requireMixed := true
	requireNum := true
	requireSymbol := true

	// Single numeric parameter overrides only the length and keeps defaults.
	if len(params) == 1 {
		if n, err := strconv.Atoi(params[0]); err == nil && n > 0 {
			minLen = n
			params = nil
		}
	}

	for _, p := range params {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
			continue
		case p == "mixed":
			requireMixed = true
		case p == "num" || p == "numbers":
			requireNum = true
		case p == "symbol" || p == "symbols":
			requireSymbol = true
		case strings.HasPrefix(p, "length:"):
			if n, err := strconv.Atoi(strings.TrimPrefix(p, "length:")); err == nil && n > 0 {
				minLen = n
			}
		default:
			if n, err := strconv.Atoi(p); err == nil && n > 0 {
				minLen = n
			}
		}
	}

	if len(str) < minLen {
		return fmt.Errorf("The %s field must be at least %d characters.", field, minLen)
	}

	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range str {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPunct(r), unicode.IsSymbol(r):
			hasSymbol = true
		}
	}

	if requireMixed && (!hasUpper || !hasLower) {
		return fmt.Errorf("The %s field must contain at least one uppercase and one lowercase letter.", field)
	}
	if requireNum && !hasDigit {
		return fmt.Errorf("The %s field must contain at least one number.", field)
	}
	if requireSymbol && !hasSymbol {
		return fmt.Errorf("The %s field must contain at least one symbol.", field)
	}
	return nil
}
