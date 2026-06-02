package rules

import (
	"fmt"
	"regexp"
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
		wantMsg string
		maxTime time.Duration
	}{
		{"nil ok", nil, []string{`^[A-Z]+$`}, false, "", 0},
		{"no pattern", "abc", nil, true, "", 0},
		{"pattern matches", "ABC", []string{`^[A-Z]+$`}, false, "", 0},
		{"pattern mismatch", "abc", []string{`^[A-Z]+$`}, true, "", 0},
		{"non-string value", 1, []string{`^[A-Z]+$`}, true, "", 0},
		{"oversized input rejected generically", strings.Repeat("A", maxRegexInputBytes+1), []string{`^[A-Z]+$`}, true, "The code field format is invalid.", 100 * time.Millisecond},
		{"unanchored rejected (no ^)", "abc", []string{`[A-Z]+$`}, true, "", 0},
		{"unanchored rejected (no $)", "abc", []string{`^[A-Z]+`}, true, "", 0},
		{"nested quantifier rejected", "aaaa", []string{`^(a+)+$`}, true, "", 0},
		{"nested star rejected", "aaaa", []string{`^(a*)*$`}, true, "", 0},
		{"invalid syntax rejected", "abc", []string{`^(a$`}, true, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Now()
			err := RegexRule("code", tt.value, tt.params, nil)
			elapsed := time.Since(start)
			if (err != nil) != tt.wantErr {
				t.Fatalf("want err=%v, got %v", tt.wantErr, err)
			}
			if tt.wantMsg != "" && err.Error() != tt.wantMsg {
				t.Fatalf("want error %q, got %q", tt.wantMsg, err.Error())
			}
			if tt.maxTime > 0 && elapsed >= tt.maxTime {
				t.Fatalf("RegexRule took %v (>= %v)", elapsed, tt.maxTime)
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

func TestCompileAnchored_RegexCacheStopsAtMaxEntries(t *testing.T) {
	regexCacheMu.Lock()
	originalCache := regexCache
	regexCache = map[string]*regexp.Regexp{}
	regexCacheMu.Unlock()
	defer func() {
		regexCacheMu.Lock()
		regexCache = originalCache
		regexCacheMu.Unlock()
	}()

	for i := 0; i <= maxRegexCacheEntries; i++ {
		literal := fmt.Sprintf("cache_bound_%04d", i)
		re, err := compileAnchored("^" + literal + "$")
		if err != nil {
			t.Fatalf("compileAnchored pattern %d returned error: %v", i, err)
		}
		if !re.MatchString(literal) {
			t.Fatalf("compileAnchored pattern %d returned regex that does not match its literal", i)
		}
		if got := regexCacheLen(); got > maxRegexCacheEntries {
			t.Fatalf("regexCache len=%d, want <= %d", got, maxRegexCacheEntries)
		}
	}

	if got := regexCacheLen(); got != maxRegexCacheEntries {
		t.Fatalf("regexCache len=%d, want %d", got, maxRegexCacheEntries)
	}
}

func regexCacheLen() int {
	regexCacheMu.RLock()
	defer regexCacheMu.RUnlock()
	return len(regexCache)
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

// TestRegexRule_RejectsCatastrophicPatternAtCompileTime asserts that the
// classic ReDoS pattern `^(a+)+$` is refused by `compileAnchored` (the
// AST-level analyzer) and never reaches `re.MatchString`. This is the
// primary defence: Go's regexp package has no preemption, so a runtime
// timeout cannot stop a pathological match, it only abandons the
// goroutine that keeps burning CPU.
func TestRegexRule_RejectsCatastrophicPatternAtCompileTime(t *testing.T) {
	bad := `^(a+)+$`
	// Standard ReDoS attack input: many `a`s followed by a non-matching
	// suffix. On a backtracking engine this would exhibit exponential
	// time; we assert it is rejected synchronously before evaluation.
	input := strings.Repeat("a", 20) + "X"

	// Bypass RegexRule's own timeout fallback and prove the analyzer
	// rejects the pattern up-front.
	if _, err := compileAnchored(bad); err == nil {
		t.Fatalf("compileAnchored(%q) returned nil error; want analyzer rejection", bad)
	}

	// And the public rule must surface the rejection.
	start := time.Now()
	err := RegexRule("code", input, []string{bad}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("RegexRule accepted catastrophic pattern %q", bad)
	}
	// Compile-time rejection is synchronous; it should be far below
	// the 10ms evaluation timeout.
	if elapsed >= regexEvalTimeout {
		t.Fatalf("RegexRule took %v (>= %v): looks like rejection ran via the runtime timeout, not the AST analyzer", elapsed, regexEvalTimeout)
	}
}

// TestAnalyzeReDoSRisk_NestedShapes covers the AST walker directly so the
// matrix of nested-repetition shapes is exhaustive without going through
// RegexRule's own anchoring/parameter handling.
func TestAnalyzeReDoSRisk_NestedShapes(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"plain anchored char class", `^[A-Z]+$`, false},
		{"plain bounded repetition", `^a{2,5}$`, false},
		{"alternation, no nesting", `^(foo|bar)$`, false},
		{"quantified group, no inner quantifier", `^(ab)+$`, false},
		{"two sibling unbounded reps", `^a+b+$`, false},

		{"nested plus over plus", `^(a+)+$`, true},
		{"nested star over star", `^(a*)*$`, true},
		{"nested plus over star", `^(a*)+$`, true},
		{"nested unbounded repeat", `^(a{1,})+$`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := analyzeReDoSRisk(tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Fatalf("analyzeReDoSRisk(%q) err=%v, wantErr=%v", tt.pattern, err, tt.wantErr)
			}
		})
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
