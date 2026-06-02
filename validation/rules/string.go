package rules

import (
	"context"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Pre-compiled regexes to avoid recompilation on every call.
//
// Note: the previous hand-rolled emailRegex/urlRegex were removed — the
// rules now rely on net/mail.ParseAddress and net/url.Parse respectively,
// which match RFC 5322 / 3986 correctness far better than a regex ever can.
var (
	alphaRegex     = regexp.MustCompile(`^[a-zA-Z]+$`)
	alphaDashRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	alphaNumRegex  = regexp.MustCompile(`^[a-zA-Z0-9]+$`)
)

// urlResolveTimeout caps the time URLPublicRule will spend resolving a host
// via net.LookupIP. A previous implementation used the unbounded
// net.LookupIP(host) which could block a handler indefinitely on a slow or
// hostile resolver.
const urlResolveTimeout = 5 * time.Second

// urlAllowedSchemes enforces that only http/https are accepted by URLRule;
// ftp, file, gopher, javascript, etc. are rejected.
var urlAllowedSchemes = map[string]struct{}{
	"http":  {},
	"https": {},
}

// privateNetworks contains CIDR ranges considered private/internal
var privateNetworks []*net.IPNet

func init() {
	cidrs := []string{
		"0.0.0.0/8",      // "this host" / unspecified, routes to localhost on Linux
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // Link-local
		"100.64.0.0/10",  // CGNAT / cloud shared address space
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique local
		"fe80::/10",      // IPv6 link-local
	}
	for _, cidr := range cidrs {
		_, network, _ := net.ParseCIDR(cidr)
		privateNetworks = append(privateNetworks, network)
	}
}

// StringRule validates that a value is a string
func StringRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil // Let required rule handle nil
	}

	if _, ok := value.(string); !ok {
		return fmt.Errorf("The %s field must be a string.", field)
	}
	return nil
}

// EmailRule validates that a value is a valid email address.
//
// The rule delegates entirely to net/mail.ParseAddress — the hand-rolled
// regex that used to gate this check was consistently wrong (it rejected
// perfectly valid addresses like quoted local-parts and new TLDs) and has
// been dropped. ParseAddress is the canonical RFC 5322 check.
func EmailRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a string.", field)
	}

	if str == "" {
		return fmt.Errorf("The %s field must be a valid email address.", field)
	}

	addr, err := mail.ParseAddress(str)
	if err != nil || addr.Address == "" {
		return fmt.Errorf("The %s field must be a valid email address.", field)
	}

	// net/mail.ParseAddress accepts display-name wrappers like
	// "Alice <alice@example.com>"; reject those — the raw value is meant to
	// be just the address.
	if addr.Address != str {
		return fmt.Errorf("The %s field must be a valid email address.", field)
	}

	// Host must contain a dot — "user@localhost" is accepted by ParseAddress
	// but not by any real mail server.
	at := strings.LastIndex(str, "@")
	if at == -1 || !strings.Contains(str[at+1:], ".") {
		return fmt.Errorf("The %s field must be a valid email address.", field)
	}

	return nil
}

// URLRule validates that a value is an http(s) URL.
//
// Uses net/url.Parse + an explicit scheme allowlist rather than a regex —
// the regex approach silently accepted things like `https:foo` (no //) and
// rejected valid IPv6-bracketed hosts.
func URLRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a string.", field)
	}

	if str == "" {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}

	parsed, err := url.Parse(str)
	if err != nil {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}
	if _, ok := urlAllowedSchemes[strings.ToLower(parsed.Scheme)]; !ok {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}
	return nil
}

// URLPublicRule validates that a value is a valid URL pointing to a public
// (non-internal) host. Rejects private/internal IPs: 0.0.0.0/8, 127.0.0.0/8,
// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16,
// 100.64.0.0/10, ::1, fc00::/7, fe80::/10.
// This rule is validation-time only: it resolves DNS once at validation and
// does not defend against DNS rebinding at fetch time; a fetching client must
// re-check at dial time (the httpclient denyPrivateIPs path does this).
//
// The DNS lookup is bounded by urlResolveTimeout (5s) to prevent a slow or
// adversarial resolver from hanging the calling handler indefinitely.
func URLPublicRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if err := URLRule(field, value, params, data); err != nil {
		return err
	}
	if value == nil {
		return nil
	}

	str := value.(string)
	parsed, err := url.Parse(str)
	if err != nil {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("The %s field must be a valid URL.", field)
	}

	// Bound DNS resolution so a slow resolver cannot hang the request.
	ctx, cancel := context.WithTimeout(context.Background(), urlResolveTimeout)
	defer cancel()
	resolver := net.DefaultResolver
	ips, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("The %s field must resolve to a valid host.", field)
	}

	for _, ip := range ips {
		if ip.IP.IsUnspecified() || ip.IP.IsLoopback() || ip.IP.IsPrivate() ||
			ip.IP.IsLinkLocalUnicast() || ip.IP.IsLinkLocalMulticast() {
			return fmt.Errorf("The %s field must not point to a private or internal address.", field)
		}
		for _, network := range privateNetworks {
			if network.Contains(ip.IP) {
				return fmt.Errorf("The %s field must not point to a private or internal address.", field)
			}
		}
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
		return fmt.Errorf("The %s field must be a string.", field)
	}

	if !alphaRegex.MatchString(str) {
		return fmt.Errorf("The %s field must contain only alphabetic characters.", field)
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
		return fmt.Errorf("The %s field must be a string.", field)
	}

	if !alphaDashRegex.MatchString(str) {
		return fmt.Errorf("The %s field must contain only letters, numbers, dashes, and underscores.", field)
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
		return fmt.Errorf("The %s field must be a string.", field)
	}

	if !alphaNumRegex.MatchString(str) {
		return fmt.Errorf("The %s field must contain only letters and numbers.", field)
	}
	return nil
}

// MinRule validates minimum length/size
func MinRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("The min rule requires 1 parameter.")
	}

	min, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("The min parameter must be a number.")
	}

	switch v := value.(type) {
	case string:
		if len(v) < min {
			return fmt.Errorf("The %s field must be at least %d characters.", field, min)
		}
	case int:
		if v < min {
			return fmt.Errorf("The %s field must be at least %d.", field, min)
		}
	case float64:
		if v < float64(min) {
			return fmt.Errorf("The %s field must be at least %d.", field, min)
		}
	case []interface{}:
		if len(v) < min {
			return fmt.Errorf("The %s field must have at least %d items.", field, min)
		}
	case []string:
		if len(v) < min {
			return fmt.Errorf("The %s field must have at least %d items.", field, min)
		}
	default:
		return fmt.Errorf("The %s field type is not supported for the min rule.", field)
	}

	return nil
}

// MaxRule validates maximum length/size
func MaxRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("The max rule requires 1 parameter.")
	}

	max, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("The max parameter must be a number.")
	}

	switch v := value.(type) {
	case string:
		if len(v) > max {
			return fmt.Errorf("The %s field must not exceed %d characters.", field, max)
		}
	case int:
		if v > max {
			return fmt.Errorf("The %s field must not exceed %d.", field, max)
		}
	case float64:
		if v > float64(max) {
			return fmt.Errorf("The %s field must not exceed %d.", field, max)
		}
	case []interface{}:
		if len(v) > max {
			return fmt.Errorf("The %s field must not have more than %d items.", field, max)
		}
	case []string:
		if len(v) > max {
			return fmt.Errorf("The %s field must not have more than %d items.", field, max)
		}
	default:
		return fmt.Errorf("The %s field type is not supported for the max rule.", field)
	}

	return nil
}

// SizeRule validates exact size
func SizeRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 1 {
		return fmt.Errorf("The size rule requires 1 parameter.")
	}

	size, err := strconv.Atoi(params[0])
	if err != nil {
		return fmt.Errorf("The size parameter must be a number.")
	}

	switch v := value.(type) {
	case string:
		if len(v) != size {
			return fmt.Errorf("The %s field must be exactly %d characters.", field, size)
		}
	case int:
		if v != size {
			return fmt.Errorf("The %s field must be exactly %d.", field, size)
		}
	case []interface{}:
		if len(v) != size {
			return fmt.Errorf("The %s field must have exactly %d items.", field, size)
		}
	case []string:
		if len(v) != size {
			return fmt.Errorf("The %s field must have exactly %d items.", field, size)
		}
	default:
		return fmt.Errorf("The %s field type is not supported for the size rule.", field)
	}

	return nil
}

// BetweenRule validates that a value is between min and max
func BetweenRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}

	if len(params) != 2 {
		return fmt.Errorf("The between rule requires 2 parameters.")
	}

	min, err1 := strconv.Atoi(params[0])
	max, err2 := strconv.Atoi(params[1])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("The between parameters must be numbers.")
	}

	switch v := value.(type) {
	case string:
		// Try to convert string to int for numeric validation
		if intVal, err := strconv.Atoi(v); err == nil {
			if intVal < min || intVal > max {
				return fmt.Errorf("The %s field must be between %d and %d.", field, min, max)
			}
		} else {
			// If not a number, check string length
			length := len(v)
			if length < min || length > max {
				return fmt.Errorf("The %s field must be between %d and %d characters.", field, min, max)
			}
		}
	case int:
		if v < min || v > max {
			return fmt.Errorf("The %s field must be between %d and %d.", field, min, max)
		}
	case float64:
		if v < float64(min) || v > float64(max) {
			return fmt.Errorf("The %s field must be between %d and %d.", field, min, max)
		}
	case []interface{}:
		length := len(v)
		if length < min || length > max {
			return fmt.Errorf("The %s field must have between %d and %d items.", field, min, max)
		}
	default:
		return fmt.Errorf("The %s field type is not supported for the between rule.", field)
	}

	return nil
}
