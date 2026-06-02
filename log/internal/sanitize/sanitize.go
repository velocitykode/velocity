// Package sanitize provides log-output sanitisation primitives shared
// by every Velocity log driver.
//
// The framework's stock drivers (console, file/daily) emit a single
// human-readable line per record by concatenating a level prefix, the
// caller-supplied message, and key/value pairs formatted with
// fmt.Sprintf("%v=%v", k, v). None of those interpolations escape
// control characters. Any of them that includes attacker-controlled
// input (the URL path is the canonical example, but request headers,
// query strings, error messages wrapping user data, etc. all qualify)
// can therefore emit literal CR / LF / ESC bytes that:
//
//   - forge additional log records ("CRLF log injection", CWE-117) so
//     a single attacker request produces multiple SIEM-visible lines,
//     including fake FATAL entries with arbitrary timestamps;
//
//   - hijack the operator's terminal via ANSI escapes when tailing a
//     file driver's output, or when watching stdout in dev.
//
// String walks the input once and replaces every byte less than 0x20
// (except TAB so columnar logs still align), every 0x7F (DEL), and
// CSI single-byte 0x9B with a \xHH escape sequence. It also escapes
// the UTF-8 encodings of U+0085 (NEL), U+2028 (line separator), and
// U+2029 (paragraph separator), because downstream UTF-8-aware log
// viewers and normalisers can treat them as real line breaks. Other
// bytes pass through verbatim. The output is a printable, single-line
// representation of the input that preserves the visible content but
// strips every control-character side channel.
//
// Value is the entry point drivers use to sanitise an arbitrary
// fmt.Sprintf("%v", v) interpolation, and KV sanitises both halves of
// a key/value pair (the key matters too in case it is itself
// user-tainted).
package sanitize

import "strings"

// String returns s with control characters replaced by their hex
// escape form (e.g. "\x0a" for newline). TAB (0x09) is preserved so
// columnar text logs remain aligned. The output never contains a raw
// CR, LF, ESC, or other byte less than 0x20.
//
// String is allocation-free when the input already contains no
// control characters: it walks the input once and returns the
// original string if no escape is required. Hot-path callers (every
// log line) therefore pay only one read pass when nothing needs
// rewriting.
func String(s string) string {
	if !needsEscape(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		if esc, n := unicodeLineBreakEscape(s, i); n > 0 {
			b.WriteString(esc)
			i += n - 1
			continue
		}
		c := s[i]
		if shouldEscape(c) {
			b.WriteString(hexEscape(c))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// Value sanitises an arbitrary string-formatted value before it is
// emitted into a log line. Drivers should call Value on the result of
// fmt.Sprintf("%v", v) (or equivalent) for every kv value and for
// the caller-supplied message.
//
// Currently identical to String, but kept as a named entry point so
// the driver call sites read intentionally ("sanitise this kv value
// before emit") and so the policy can evolve (e.g. add length cap or
// secret redaction) without churning every driver.
func Value(s string) string {
	return String(s)
}

// KV sanitises a key/value pair. Both halves are run through String;
// keys are sanitised too because nothing in the framework prevents a
// caller from passing a user-tainted string as a kv key, and a CRLF
// in the key would forge a log line just as effectively as one in
// the value.
func KV(key, value string) (string, string) {
	return String(key), String(value)
}

// needsEscape reports whether s contains any byte or UTF-8 line-break
// sequence that String would rewrite. Used to skip the Builder
// allocation on the common path where the input is already printable.
func needsEscape(s string) bool {
	for i := 0; i < len(s); i++ {
		if _, n := unicodeLineBreakEscape(s, i); n > 0 {
			return true
		}
		if shouldEscape(s[i]) {
			return true
		}
	}
	return false
}

// shouldEscape reports whether byte c must be replaced with a hex
// escape. UTF-8 line-break sequences for U+0085, U+2028, and U+2029
// are handled by String before this byte-level policy is applied.
// The byte policy:
//
//   - bytes < 0x20 are control characters and are escaped, except
//     TAB (0x09) which is preserved so columnar logs still align;
//   - 0x7F (DEL) is escaped;
//   - 0x9B (CSI, ANSI single-byte control sequence introducer) is
//     escaped because terminals interpret it the same as ESC '['.
//
// All other bytes (including 0x20-0x7E ASCII printables, 0x80-0x9A
// and 0x9C-0xFF, which covers UTF-8 continuation bytes) pass through
// unchanged so non-ASCII text in URLs, user-agents, etc. is logged
// verbatim.
func shouldEscape(c byte) bool {
	if c == '\t' {
		return false
	}
	if c < 0x20 {
		return true
	}
	if c == 0x7F {
		return true
	}
	if c == 0x9B {
		return true
	}
	return false
}

// unicodeLineBreakEscape reports whether s[i:] starts with a UTF-8
// encoded Unicode line/paragraph separator that downstream log viewers
// may treat as a record boundary.
func unicodeLineBreakEscape(s string, i int) (string, int) {
	if len(s)-i >= 2 && s[i] == 0xc2 && s[i+1] == 0x85 {
		return `\u0085`, 2
	}
	if len(s)-i >= 3 && s[i] == 0xe2 && s[i+1] == 0x80 {
		switch s[i+2] {
		case 0xa8:
			return `\u2028`, 3
		case 0xa9:
			return `\u2029`, 3
		}
	}
	return "", 0
}

// hexEscape returns the two-byte \xHH form for c. Pre-rendered for
// the 0x00-0x1F + 0x7F + 0x9B set so String avoids fmt.Sprintf in the
// hot path.
func hexEscape(c byte) string {
	const hex = "0123456789abcdef"
	return `\x` + string([]byte{hex[c>>4], hex[c&0x0f]})
}
