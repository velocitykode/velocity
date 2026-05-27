package drivers

import (
	"fmt"
	"regexp"
	"strings"
)

// RawColumn represents a trusted raw SQL expression to project in the
// SELECT list, optionally with bound parameters. It is produced by
// Query[T].SelectRaw and surfaced through SelectQuery.RawColumns so
// grammars can emit the expression verbatim while still parameterising
// values. Drivers that use numbered placeholders (e.g. PostgreSQL with
// $N) are responsible for renumbering any "?" placeholders in Expr at
// compile time.
//
// RawColumn carries no SQL parsing of its own. The caller takes full
// responsibility for the expression. Use this only when the higher-level
// Select whitelist cannot express the projection (window functions,
// arbitrary SQL, dialect-specific syntax).
type RawColumn struct {
	Expr string
	Args []any
}

// selectExprRegex defines the only "expression-shaped" projections
// accepted by Query[T].Select (and by grammars at compile time).
//
// The function name is restricted to an EXPLICIT closed set of the
// five SQL standard aggregates: COUNT, SUM, AVG, MIN, MAX. Every
// other function (CONCAT, VERSION, CURRENT_DATABASE, PG_SLEEP, USER,
// LOAD_FILE, NOW, IF, LENGTH, LOWER, SUBSTR, ...) is rejected here.
// This is an allowlist by design: a regex like ^[A-Z_]+\(...\) would
// pass dangerous information-disclosure / side-effecting functions
// even though the character class blocks quotes and keywords.
//
// Case is significant: only uppercase aggregate names match. Lowercase
// or mixed-case variants (count, Sum) are rejected for predictability;
// callers who want flexible casing should use SelectRaw.
//
// Accepted shapes (wildcard "*" and plain identifiers are handled
// outside this regex):
//
//   - COUNT(*)
//   - COUNT(id)
//   - SUM(amount)
//   - AVG(price)
//   - MIN(orders.total) AS min_total
//   - MAX(price) as max_price (case-insensitive AS, alias required)
//
// The allowlist intentionally excludes:
//
//   - Quotes ('"`), backticks, semicolons, comments (--, /* */)
//   - Keywords that enable secondary statements (SELECT, UNION, FROM,
//     WHERE, JOIN, etc.). The character class inside the parens
//     forbids them by construction (no letters that could spell them
//     out together with the punctuation a sub-statement requires).
//   - All non-aggregate functions, including identity / introspection
//     (VERSION, USER, CURRENT_DATABASE), I/O (LOAD_FILE), timing
//     (PG_SLEEP, NOW), conditional (IF, CASE), string (CONCAT, LOWER,
//     LENGTH, SUBSTR), and any user-defined function.
//   - Subqueries, arithmetic, CASE expressions, COALESCE-with-quoted-defaults
//
// Callers needing escape hatches must use Query[T].SelectRaw with
// bound parameters.
var selectExprRegex = regexp.MustCompile(`^(?:COUNT|SUM|AVG|MIN|MAX)\([a-zA-Z0-9_*., ]*\)(\s+(?:AS|as|As|aS)\s+[a-zA-Z0-9_]+)?$`)

// forbiddenSelectTokens are substrings whose presence in a projection
// expression is always an injection signal, regardless of where they
// appear. Checked before regex match for a fast, unambiguous reject.
var forbiddenSelectTokens = []string{
	"'", `"`, "`", ";", "--", "/*", "*/", "\x00", "\r", "\n",
}

// forbiddenSelectKeywords are SQL keywords (case-insensitive) banned
// anywhere in a projection expression. The character class in
// selectExprRegex already forbids them implicitly, but checking
// explicitly gives a clearer error and protects future regex tweaks.
var forbiddenSelectKeywords = []string{
	"select", "union", "insert", "update", "delete", "drop",
	"truncate", "exec", "execute", "from", "where", "join",
	"into", "values", "alter", "create", "grant", "revoke",
}

// ValidateSelectColumn returns nil iff the supplied column projection
// is safe to emit verbatim in a SELECT list. Plain identifiers (validated
// by orm.validateIdentifier upstream) are accepted as-is; expressions
// containing "(" must match the aggregate whitelist regex AND contain
// none of the forbidden tokens/keywords.
//
// This is the single source of truth for projection validation and is
// invoked both at Query[T].Select-time (early reject in user code) and
// at CompileSelect-time (defence in depth against any code path that
// constructs SelectQuery directly).
func ValidateSelectColumn(col string) error {
	if col == "*" {
		return nil
	}

	for _, tok := range forbiddenSelectTokens {
		if strings.Contains(col, tok) {
			return fmt.Errorf("orm: select column %q contains forbidden token %q", col, tok)
		}
	}

	hasParen := strings.Contains(col, "(")
	if !hasParen {
		// Plain-identifier emission is handled by QuoteIdentifier;
		// the caller is expected to have run validateIdentifier on
		// it. We do not re-run it here because this file lives in
		// drivers and validateIdentifier lives in orm. Drivers that
		// receive a plain identifier through this path will quote
		// it correctly; the upstream Select() path has already
		// rejected anything malformed.
		return nil
	}

	lower := strings.ToLower(col)
	for _, kw := range forbiddenSelectKeywords {
		if containsKeyword(lower, kw) {
			return fmt.Errorf("orm: select column %q contains forbidden keyword %q", col, kw)
		}
	}

	if !selectExprRegex.MatchString(col) {
		return fmt.Errorf("orm: select column %q is not a permitted aggregate expression; use SelectRaw for arbitrary SQL", col)
	}

	return nil
}

// containsKeyword reports whether s contains kw as a whole word.
// Word boundary is defined as start/end of string or any character
// that is not [a-z0-9_].
func containsKeyword(s, kw string) bool {
	idx := 0
	for {
		i := strings.Index(s[idx:], kw)
		if i < 0 {
			return false
		}
		absStart := idx + i
		absEnd := absStart + len(kw)
		if absStart > 0 {
			c := s[absStart-1]
			if isWordByte(c) {
				idx = absEnd
				continue
			}
		}
		if absEnd < len(s) {
			c := s[absEnd]
			if isWordByte(c) {
				idx = absEnd
				continue
			}
		}
		return true
	}
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// sanitizeForComment strips characters that could close a /* ... */
// SQL comment, so an error message can be embedded safely. Used by
// drivers that emit a poison SELECT 1 WHERE 1=0 with a diagnostic
// comment when ValidateSelectColumn fails at compile time.
func sanitizeForComment(s string) string {
	s = strings.ReplaceAll(s, "*/", "* /")
	s = strings.ReplaceAll(s, "/*", "/ *")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
