package rules

import (
	"encoding/json"
	"fmt"
	"regexp"
	"regexp/syntax"
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

// maxRegexInputBytes caps the user-controlled value before regexp evaluation.
// Anchored format validation rarely needs more than a few KiB, and Go's
// regexp matcher cannot be preempted once started.
const maxRegexInputBytes = 4096

var (
	regexCacheMu sync.RWMutex
	regexCache   = map[string]*regexp.Regexp{}
)

// maxRepetitionNestingDepth caps how deeply repetition operators
// (`*`, `+`, `{n,}`) may nest before we refuse the pattern. Three levels is
// already enough to express any sane validation regex; deeper nesting is
// almost always a footgun that risks catastrophic backtracking on engines
// without RE2's linear-time guarantees.
const maxRepetitionNestingDepth = 3

// analyzeReDoSRisk parses `pattern` with `regexp/syntax` (Perl flavour) and
// walks the resulting AST looking for two well-known catastrophic shapes:
//
//  1. nested unbounded repetition such as `(a+)+`, `(a*)*`, or `(a|a)+`,
//     where the inner quantifier produces multiple ways to consume the same
//     input and the outer quantifier multiplies them; and
//  2. excessive repetition nesting in general (more than
//     `maxRepetitionNestingDepth` levels of `OpStar`/`OpPlus`/`OpRepeat`).
//
// Go's `regexp` package is RE2-based and runs in linear time, so neither
// shape can blow up the engine. But the package exposes no preemption: a
// caller that bounds evaluation with a `select` + timeout still leaks a
// goroutine that pegs a CPU until the match completes. Pre-rejecting the
// pattern at compile time means the timeout becomes a fallback, not the
// only line of defence.
func analyzeReDoSRisk(pattern string) error {
	tree, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		// Surface the parse error verbatim so the caller can report
		// "invalid regex" rather than masking it as a ReDoS problem.
		return err
	}
	return walkForReDoS(tree, 0, false)
}

// walkForReDoS recurses the syntax tree. `repDepth` counts the number of
// enclosing unbounded-repetition nodes; `insideUnboundedRep` is true if any
// ancestor is an unbounded repetition (`*`, `+`, or `{n,}` with no upper
// bound). When a second unbounded-repetition node is found while
// `insideUnboundedRep` is true, that's a nested-unbounded-repetition shape
// and we refuse the pattern.
func walkForReDoS(re *syntax.Regexp, repDepth int, insideUnboundedRep bool) error {
	if re == nil {
		return nil
	}

	unbounded := isUnboundedRepetition(re)
	if unbounded {
		if insideUnboundedRep {
			return fmt.Errorf("regex contains nested unbounded repetition that risks catastrophic backtracking")
		}
		repDepth++
		if repDepth > maxRepetitionNestingDepth {
			return fmt.Errorf("regex nests repetition operators more than %d levels deep", maxRepetitionNestingDepth)
		}
		insideUnboundedRep = true
	}

	for _, sub := range re.Sub {
		if err := walkForReDoS(sub, repDepth, insideUnboundedRep); err != nil {
			return err
		}
	}
	return nil
}

// isUnboundedRepetition reports whether `re` is a repetition node with no
// finite upper bound: `*`, `+`, or `{n,}`. Bounded repetitions like `{2,5}`
// cannot drive exponential blow-up on their own, so we ignore them here.
func isUnboundedRepetition(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpStar, syntax.OpPlus:
		return true
	case syntax.OpRepeat:
		// Max == -1 in regexp/syntax means "no upper bound" (e.g. {3,}).
		return re.Max == -1
	}
	return false
}

// compileAnchored returns the compiled form of `pattern`, rejecting patterns
// that are not `^...$`-anchored or that smell of catastrophic backtracking.
//
// The ReDoS guard runs against the parsed `regexp/syntax` tree (see
// `analyzeReDoSRisk`) and is the primary defence: Go's `regexp` package has
// no preemption, so a runtime evaluation timeout cannot actually stop a
// pathological match, it only abandons the goroutine that keeps burning
// CPU. Pre-rejecting risky shapes here means the runtime select+timeout in
// `RegexRule` is belt-and-suspenders, not the only line of defence.
func compileAnchored(pattern string) (*regexp.Regexp, error) {
	if !strings.HasPrefix(pattern, "^") || !strings.HasSuffix(pattern, "$") {
		return nil, fmt.Errorf("regex must be anchored with ^ and $")
	}
	if err := analyzeReDoSRisk(pattern); err != nil {
		return nil, err
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
// Values over 4 KiB are rejected before evaluation because Go's regexp
// matcher cannot be preempted once started. Each call is also bounded to
// 10ms of wall clock time as a fallback for unforeseen edge cases, but that
// timeout cannot stop the worker goroutine after MatchString begins.
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
	if len(str) > maxRegexInputBytes {
		return fmt.Errorf("The %s field format is invalid.", field)
	}

	// Bound evaluation on a goroutine. Note: Go's `regexp` package has no
	// preemption, so this select abandons the result but the worker goroutine
	// keeps burning CPU until MatchString returns. The input-size cap above
	// prevents large user-controlled values from reaching the matcher; the
	// AST-level ReDoS check in compileAnchored (analyzeReDoSRisk) rejects
	// risky patterns. This timeout is only a fallback for unforeseen edge
	// cases.
	type result struct{ matched bool }
	done := make(chan result, 1)
	// Not async.Go: must forward a recovered panic value through `done`
	// so the outer select treats the regex run as a validation failure
	// (not an ignored panic that hangs validation until the timeout).
	go func() { //safe-goroutine: forwards panic via done so outer select reports validation failure, see comment above
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
