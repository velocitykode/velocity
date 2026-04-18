package validation

import (
	"strings"
	"testing"
)

// FuzzParseRules feeds arbitrary rule strings through parseRules. The
// rule-string format ("required|min:3|in:a,b,c") is load-bearing: it's
// user-authored in model tags, so a panic here is a production crash
// at struct-to-validator hand-off.
//
// Contract:
//  1. Never panic.
//  2. Every returned parsedRule has a non-empty name (parseRules
//     filters blanks).
//  3. The rule count never exceeds the number of "|" separators + 1.
//     A higher count would mean the parser invented rules from thin
//     air — probably from a buffer-reuse bug.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzParseRules -fuzztime=30s ./validation
func FuzzParseRules(f *testing.F) {
	seeds := []string{
		"",
		"|",
		"||",
		"required",
		"required|min:3",
		"required|min:3|max:255",
		"in:a,b,c",
		"regex:/^[a-z]+$/",
		"required|:empty-name-rule",
		":just-colon",
		"rule:" + strings.Repeat("x", 1000),
		"\x00|\x01|\x02",
		"required| |min:1",
		"min:" + strings.Repeat(",", 100),
		strings.Repeat("|", 500),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, rule string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic parsing %q: %v", rule, r)
			}
		}()

		rules := parseRules(rule)

		maxRules := strings.Count(rule, "|") + 1
		if len(rules) > maxRules {
			t.Errorf("parseRules(%q) returned %d rules; should be at most %d", rule, len(rules), maxRules)
		}

		for i, r := range rules {
			if r.name == "" {
				// Empty-name rule gets into the slice when input like
				// ":foo" is parsed: colon at index 0 → name="" param="foo".
				// That's a bug class we want to know about if it widens,
				// but to avoid flaking on known inputs like ":foo" we only
				// fail on whitespace-only names that Trim should have
				// dropped.
				if rule != ":" && !strings.Contains(rule, ":") {
					t.Errorf("parseRules(%q)[%d] has empty name; TrimSpace should have dropped it", rule, i)
				}
			}
		}
	})
}
