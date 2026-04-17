package rules

import (
	"strings"
	"testing"
	"time"
)

func TestRegexRule_BasicMatch(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		params  []string
		wantErr bool
	}{
		{"nil ok", nil, []string{`^[A-Z]+$`}, false},
		{"no pattern", "abc", nil, true},
		{"pattern matches", "ABC", []string{`^[A-Z]+$`}, false},
		{"pattern mismatch", "abc", []string{`^[A-Z]+$`}, true},
		{"non-string value", 1, []string{`^[A-Z]+$`}, true},
		{"unanchored rejected (no ^)", "abc", []string{`[A-Z]+$`}, true},
		{"unanchored rejected (no $)", "abc", []string{`^[A-Z]+`}, true},
		{"nested quantifier rejected", "aaaa", []string{`^(a+)+$`}, true},
		{"nested star rejected", "aaaa", []string{`^(a*)*$`}, true},
		{"invalid syntax rejected", "abc", []string{`^(a$`}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RegexRule("code", tt.value, tt.params, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestRegexRule_RejoinsComma verifies the rule parser's split-on-comma
// does not break patterns that contain a literal comma.
func TestRegexRule_RejoinsComma(t *testing.T) {
	// "^[a,b]+$" matches "a,b" — caller sends it as ["^[a", "b]+$"] because
	// the outer parser splits on ",".
	err := RegexRule("code", "a,b", []string{`^[a`, `b]+$`}, nil)
	if err != nil {
		t.Fatalf("expected pattern to rejoin on commas and match, got: %v", err)
	}
}

// TestRegexRule_CatastrophicBacktrackingTimeout synthesizes a pattern that
// would cause catastrophic backtracking in a PCRE engine. Go's RE2 is
// linear-time so the match itself will succeed fast; we assert that our
// suspicious-shape detector refuses the pattern *before* it is compiled.
func TestRegexRule_CatastrophicBacktrackingTimeout(t *testing.T) {
	// Nested quantifier is the classic shape; we reject up-front.
	bad := `^(a+)+$`
	input := strings.Repeat("a", 30) + "b" // classic catastrophic-input suffix

	start := time.Now()
	err := RegexRule("code", input, []string{bad}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected regex rule to reject catastrophic pattern %q", bad)
	}
	// The timeout cap is 10ms; nested-quantifier rejection is synchronous
	// (regex never compiles) so we should be well under the cap.
	if elapsed > 100*time.Millisecond {
		t.Fatalf("regex rule took too long to reject pattern: %v", elapsed)
	}
}

func TestJSONRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"valid object", `{"a":1}`, false},
		{"valid array", `[1,2,3]`, false},
		{"valid string", `"hello"`, false},
		{"valid number", `42`, false},
		{"invalid", `{not-json}`, true},
		{"empty", ``, true},
		{"non-string", 42, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := JSONRule("body", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestUUIDRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"lowercase v4", "f47ac10b-58cc-4372-a567-0e02b2c3d479", false},
		{"uppercase", "F47AC10B-58CC-4372-A567-0E02B2C3D479", false},
		{"nil UUID", "00000000-0000-0000-0000-000000000000", false},
		{"missing hyphens", "f47ac10b58cc4372a5670e02b2c3d479", true},
		{"too short", "f47ac10b-58cc-4372-a567-0e02b2c3d47", true},
		{"non-hex", "f47ac10b-58cc-4372-a567-0e02b2c3d47g", true},
		{"empty", "", true},
		{"non-string", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := UUIDRule("id", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestULIDRule(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		wantErr bool
	}{
		{"nil ok", nil, false},
		{"valid ulid", "01ARZ3NDEKTSV4RRFFQ69G5FAV", false},
		{"valid lowercase (uppercased internally)", "01arz3ndektsv4rrffq69g5fav", false},
		{"too short", "01ARZ3NDEKTSV4RRFFQ69G5FA", true},
		{"too long", "01ARZ3NDEKTSV4RRFFQ69G5FAVV", true},
		{"forbidden letter I", "01ARZ3NDEKTSV4RRFFQ69G5FAI", true},
		{"empty", "", true},
		{"non-string", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ULIDRule("id", tt.value, nil, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
		})
	}
}
