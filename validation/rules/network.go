package rules

import (
	"fmt"
	"net"
)

// IPRule accepts any IPv4 or IPv6 literal.
func IPRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return fmt.Errorf("The %s field must be a valid IP address.", field)
	}
	ip := net.ParseIP(str)
	if ip == nil {
		return fmt.Errorf("The %s field must be a valid IP address.", field)
	}
	return nil
}

// IPv4Rule accepts only IPv4 literals.
func IPv4Rule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return fmt.Errorf("The %s field must be a valid IPv4 address.", field)
	}
	ip := net.ParseIP(str)
	if ip == nil || ip.To4() == nil {
		return fmt.Errorf("The %s field must be a valid IPv4 address.", field)
	}
	return nil
}

// IPv6Rule accepts only IPv6 literals (rejects IPv4-in-IPv6 like ::ffff:1.2.3.4
// is still accepted because Go treats it as IPv6; reject To4 forms that do not
// also have a 16-byte representation).
func IPv6Rule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok || str == "" {
		return fmt.Errorf("The %s field must be a valid IPv6 address.", field)
	}
	ip := net.ParseIP(str)
	if ip == nil || ip.To16() == nil || ip.To4() != nil {
		return fmt.Errorf("The %s field must be a valid IPv6 address.", field)
	}
	return nil
}
