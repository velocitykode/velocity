package rules

import (
	"fmt"
	"time"
)

// dateLayouts is the ordered list of RFC and common layouts tried by DateRule
// before the rule gives up. Each attempts ParseInLocation(UTC) to avoid TZ
// surprises.
var dateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"02/01/2006",
	"01/02/2006",
}

// DateRule validates that a value parses as a date/time in one of several
// common layouts (RFC3339, ISO 8601, "YYYY-MM-DD HH:MM:SS", "YYYY-MM-DD",
// and two slash variants).
//
// If the field should accept exactly one layout, use `date_format:<layout>`.
func DateRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok {
		if _, ok := value.(time.Time); ok {
			return nil
		}
		return fmt.Errorf("The %s field must be a valid date.", field)
	}
	if str == "" {
		return fmt.Errorf("The %s field must be a valid date.", field)
	}
	for _, layout := range dateLayouts {
		if _, err := time.ParseInLocation(layout, str, time.UTC); err == nil {
			return nil
		}
	}
	return fmt.Errorf("The %s field must be a valid date.", field)
}

// DateFormatRule validates that a value parses using a single Go time layout.
//
// Usage: date_format:2006-01-02 or date_format:2006-01-02T15:04:05Z07:00
//
// The layout is the Go reference-time layout (not strftime).
func DateFormatRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 || params[0] == "" {
		return fmt.Errorf("The date_format rule requires a layout parameter.")
	}
	// Allow commas inside the layout by re-joining params (rule parser splits on ",").
	layout := params[0]
	for i := 1; i < len(params); i++ {
		layout += "," + params[i]
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must match the format %s.", field, layout)
	}
	if _, err := time.ParseInLocation(layout, str, time.UTC); err != nil {
		return fmt.Errorf("The %s field must match the format %s.", field, layout)
	}
	return nil
}

// TimezoneRule validates that the value is a valid IANA timezone identifier
// known to the host (e.g. "UTC", "America/New_York", "Europe/London").
func TimezoneRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return fmt.Errorf("The %s field must be a valid timezone.", field)
	}
	if _, err := time.LoadLocation(str); err != nil {
		return fmt.Errorf("The %s field must be a valid timezone.", field)
	}
	return nil
}
