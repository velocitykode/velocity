package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// uuidRegex matches UUID v1-v8 (8-4-4-4-12 hex, case-insensitive) with the
// canonical variant nibble (RFC 4122 sec 4.1.1). Non-RFC-4122 UUIDs (all-zero
// or all-ones) are also accepted — they are technically valid "nil" and "max"
// UUIDs that some systems emit.
var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ulidRegex matches Crockford-base32 ULIDs (26 chars, no I/L/O/U).
var ulidRegex = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)

// regexCompileTimeout caps how long a user-supplied regex is allowed to run
// against a single value before we abandon it. Catastrophic backtracking
// patterns (`(a+)+`) can take exponential time on adversarial input; even
// though we refuse the most obvious shapes up front, this is a belt-and-
// braces bound.
const regexEvalTimeout = 10 * time.Millisecond

var (
	regexCacheMu sync.RWMutex
	regexCache   = map[string]*regexp.Regexp{}
)

// suspiciousNested catches the most common catastrophic-backtracking shapes:
// a quantified group whose body ends in another quantifier, e.g. (a+)+,
// (.*)*, (\w+)+. This is not a full safety proof — Go's RE2 engine is
// linear-time for the regexes it supports, but we still guard callers who
// embed a Perl-compatible `(?P<name>...)` pattern or similar.
var suspiciousNested = regexp.MustCompile(`\([^()]*[+*][^()]*\)[+*]`)

// compileAnchored returns the compiled form of `pattern`, rejecting patterns
// that are not `^...$`-anchored or that smell of catastrophic backtracking.
func compileAnchored(pattern string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
		return nil, fmt.Errorf("regex must be anchored with ^ and $")
	}
	if suspiciousNested.MatchString(pattern) {
		return nil, fmt.Errorf("regex contains nested quantifiers that risk catastrophic backtracking")
	}

	regexCacheMu.RLock()
	re, ok := regexCache[pattern]
	regexCacheMu.RUnlock()
	if ok {
		return re, nil
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexCacheMu.Lock()
	regexCache[pattern] = re
	regexCacheMu.Unlock()
	return re, nil
}

// RegexRule validates that the value fully matches a caller-supplied regex.
//
// Usage: regex:^[A-Z]{3}-\d{4}$
//
// The pattern MUST be anchored (start with `^` and end with `$`) and MUST
// NOT contain obvious catastrophic-backtracking shapes like `(X+)+`. Each
// call is also bounded to 10ms of wall clock time to cap runaway evaluation.
func RegexRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	if len(params) < 1 || params[0] == "" {
		return fmt.Errorf("The regex rule requires a pattern parameter.")
	}

	// Rule parser splits on `,`; preserve commas inside the pattern.
	pattern := strings.Join(params, ",")

	re, err := compileAnchored(pattern)
	if err != nil {
		return fmt.Errorf("The %s field has an invalid regex rule.", field)
	}

	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field format is invalid.", field)
	}

	// Bound evaluation on a goroutine.
	type result struct{ matched bool }
	done := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- result{matched: false}
			}
		}()
		done <- result{matched: re.MatchString(str)}
	}()

	select {
	case r := <-done:
		if !r.matched {
			return fmt.Errorf("The %s field format is invalid.", field)
		}
		return nil
	case <-time.After(regexEvalTimeout):
		return fmt.Errorf("The %s field format is invalid.", field)
	}
}

// JSONRule validates that the string value parses as JSON.
func JSONRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a valid JSON string.", field)
	}
	var tmp interface{}
	if err := json.Unmarshal([]byte(str), &tmp); err != nil {
		return fmt.Errorf("The %s field must be a valid JSON string.", field)
	}
	return nil
}

// UUIDRule validates that a value is a canonical UUID (8-4-4-4-12 hex).
func UUIDRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok || !uuidRegex.MatchString(str) {
		return fmt.Errorf("The %s field must be a valid UUID.", field)
	}
	return nil
}

// ULIDRule validates that a value is a 26-char Crockford-base32 ULID.
func ULIDRule(field string, value interface{}, params []string, data map[string]interface{}) error {
	if value == nil {
		return nil
	}
	str, ok := value.(string)
	if !ok {
		return fmt.Errorf("The %s field must be a valid ULID.", field)
	}
	if !ulidRegex.MatchString(strings.ToUpper(str)) {
		return fmt.Errorf("The %s field must be a valid ULID.", field)
	}
	return nil
}
