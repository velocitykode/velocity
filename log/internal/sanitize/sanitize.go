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
// String walks the input once (rune by rune) and replaces every byte
// less than 0x20 (except TAB so columnar logs still align) and every
// 0x7F (DEL) with a \xHH escape sequence. The CSI control is handled
// rune-aware: the Unicode control U+009B (UTF-8 0xC2 0x9B) is escaped
// to its U+009B form (backslash-u-009b) because terminals interpret it
// like ESC '[', while a bare or otherwise-invalid 0x9B byte is escaped
// to \x9b. A 0x9B byte that is a legitimate UTF-8 continuation byte
// (e.g. U+011B encodes 0xC4 0x9B) passes through untouched. Every other
// high byte (valid multi-byte rune or lone non-0x9B byte) also passes
// through verbatim. It also escapes the UTF-8 encodings of U+0085
// (NEL), U+2028 (line separator), and U+2029 (paragraph separator),
// because downstream UTF-8-aware log viewers and normalisers can treat
// them as real line breaks. The output is a printable, single-line
// representation of the input that preserves the visible content but
// strips every control-character side channel.
//
// Value is the entry point drivers use to sanitise an arbitrary
// fmt.Sprintf("%v", v) interpolation, and KV sanitises both halves of
// a key/value pair (the key matters too in case it is itself
// user-tainted).
package sanitize

import (
	"strings"
	"unicode/utf8"
)

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
	for i := 0; i < len(s); {
		if esc, n := unicodeLineBreakEscape(s, i); n > 0 {
			b.WriteString(esc)
			i += n
			continue
		}
		c := s[i]
		if c < 0x80 {
			// ASCII: apply the single-byte control policy.
			if shouldEscape(c) {
				b.WriteString(hexEscape(c))
			} else {
				b.WriteByte(c)
			}
			i++
			continue
		}
		// High byte: decode a rune only to resolve the 0x9B ambiguity.
		// Every other high byte keeps the historic passthrough so
		// non-ASCII text (UTF-8 or legacy bytes) is logged verbatim.
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x9B:
			// Real Unicode CSI U+009B (UTF-8 0xC2 0x9B): terminals
			// interpret it like ESC '['. Escape it.
			b.WriteString("\\u009b")
		case r == utf8.RuneError && size == 1 && c == 0x9B:
			// Bare 0x9B byte (not part of a valid rune): keep the
			// single-byte CSI escape.
			b.WriteString(hexEscape(c))
		default:
			// A valid multi-byte rune (which may legitimately contain a
			// 0x9B continuation byte, e.g. U+011B = 0xC4 0x9B) or any
			// other lone high byte: pass through unchanged.
			b.WriteString(s[i : i+size])
		}
		i += size
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

// needsEscape reports whether s contains any byte or UTF-8 sequence that
// String would rewrite. Used to skip the Builder allocation on the
// common path where the input is already printable. The scan is
// rune-aware so a 0x9B continuation byte inside a valid multi-byte rune
// is not mistaken for the CSI control.
func needsEscape(s string) bool {
	for i := 0; i < len(s); {
		if _, n := unicodeLineBreakEscape(s, i); n > 0 {
			return true
		}
		c := s[i]
		if c < 0x80 {
			if shouldEscape(c) {
				return true
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x9B:
			return true // U+009B CSI control (0xC2 0x9B)
		case r == utf8.RuneError && size == 1 && c == 0x9B:
			return true // bare 0x9B byte
		}
		i += size
	}
	return false
}

// shouldEscape reports whether an ASCII byte c (c < 0x80) must be
// replaced with a hex escape. String and needsEscape only call it for
// the ASCII range; high bytes (>= 0x80) are handled by the rune-aware
// branch in String, which passes valid multi-byte runes and lone high
// bytes through verbatim and special-cases only U+009B / bare 0x9B. The
// ASCII byte policy:
//
//   - bytes < 0x20 are control characters and are escaped, except
//     TAB (0x09) which is preserved so columnar logs still align;
//   - 0x7F (DEL) is escaped.
//
// All other ASCII bytes (0x20-0x7E printables) pass through unchanged.
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
	return false
}

// unicodeLineBreakEscape reports whether s[i:] starts with a UTF-8
// encoded Unicode line/paragraph separator that downstream log viewers
// may treat as a record boundary.
func unicodeLineBreakEscape(s string, i int) (string, int) {
	if len(s)-i >= 2 && s[i] == 0xc2 && s[i+1] == 0x85 {
		return "\\u0085", 2
	}
	if len(s)-i >= 3 && s[i] == 0xe2 && s[i+1] == 0x80 {
		switch s[i+2] {
		case 0xa8:
			return "\\u2028", 3
		case 0xa9:
			return "\\u2029", 3
		}
	}
	return "", 0
}

// hexEscape returns the two-byte \xHH form for c. Pre-rendered for the
// 0x00-0x1F + 0x7F set and for bare/invalid high bytes (e.g. a lone
// 0x9B) so String avoids fmt.Sprintf in the hot path.
func hexEscape(c byte) string {
	const hex = "0123456789abcdef"
	return `\x` + string([]byte{hex[c>>4], hex[c&0x0f]})
}
