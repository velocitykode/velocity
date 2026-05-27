package str

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Ascii transliterates a UTF-8 value to ASCII.
func Ascii(value string) string {
	// Basic transliteration - this can be expanded with a proper library
	// For now, just remove non-ASCII characters
	var result []rune
	for _, r := range value {
		if r <= 127 {
			result = append(result, r)
		}
	}
	return string(result)
}

// Slug generates a URL-friendly "slug" from the given string.
func Slug(title string, separator ...string) string {
	sep := "-"
	if len(separator) > 0 {
		sep = separator[0]
	}

	// Convert to lowercase
	slug := strings.ToLower(title)

	// Replace non-alphanumeric characters with separator
	slug = mustReplace(`[^a-z0-9]+`, slug, sep)

	// Remove leading/trailing separators
	slug = strings.Trim(slug, sep)

	// Remove consecutive separators
	// Build the pattern dynamically to handle special regex chars in separator
	pattern := regexp.QuoteMeta(sep) + `+`
	slug = mustReplace(pattern, slug, sep)

	return slug
}

// Camel converts the string to camelCase.
func Camel(value string) string {
	// Replace common delimiters with spaces
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, ".", " ")

	// Split into words
	words := strings.Fields(value)
	if len(words) == 0 {
		return ""
	}

	// First word is lowercase, rest are title case
	result := strings.ToLower(words[0])
	for i := 1; i < len(words); i++ {
		result += titleCaser.String(strings.ToLower(words[i]))
	}

	return result
}

// Kebab converts the string to kebab-case.
func Kebab(value string) string {
	return toDelimited(value, '-')
}

// Snake converts the string to snake_case.
func Snake(value string) string {
	return toDelimited(value, '_')
}

// Studly converts the string to StudlyCase (PascalCase).
func Studly(value string) string {
	// Replace common delimiters with spaces
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")
	value = strings.ReplaceAll(value, ".", " ")

	// Split into words
	words := strings.Fields(value)

	// Title case each word
	var result string
	for _, word := range words {
		result += titleCaser.String(strings.ToLower(word))
	}

	return result
}

// Helper function to convert string to delimited case
func toDelimited(s string, delimiter rune) string {
	// Handle empty string
	if s == "" {
		return s
	}

	// Replace common delimiters with the target delimiter
	s = strings.ReplaceAll(s, "_", string(delimiter))
	s = strings.ReplaceAll(s, "-", string(delimiter))
	s = strings.ReplaceAll(s, " ", string(delimiter))
	s = strings.ReplaceAll(s, ".", string(delimiter))

	// Handle camelCase and PascalCase
	var result []rune
	runes := []rune(s)

	for i := 0; i < len(runes); i++ {
		char := runes[i]

		// If current char is uppercase
		if unicode.IsUpper(char) {
			// If not at the beginning and previous char is not a delimiter
			if i > 0 && runes[i-1] != delimiter {
				// Check if this is the start of an acronym or a new word
				if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					// This is the start of a new word
					result = append(result, delimiter)
				} else if i > 0 && unicode.IsLower(runes[i-1]) {
					// Previous was lowercase, this is a new word
					result = append(result, delimiter)
				}
			}
			result = append(result, unicode.ToLower(char))
		} else if char != delimiter || (len(result) > 0 && result[len(result)-1] != delimiter) {
			// Add non-delimiter chars or single delimiter
			result = append(result, char)
		}
	}

	// Convert to string and clean up
	str := string(result)

	// Remove leading/trailing delimiters
	str = strings.Trim(str, string(delimiter))

	// Remove consecutive delimiters
	for strings.Contains(str, string(delimiter)+string(delimiter)) {
		str = strings.ReplaceAll(str, string(delimiter)+string(delimiter), string(delimiter))
	}

	return str
}

// InlineMarkdown removes all Markdown formatting from the given string.
func InlineMarkdown(str string) string {
	// Remove bold/italic
	str = mustReplace(`\*{1,3}([^*]+)\*{1,3}`, str, "$1")
	str = mustReplace(`_{1,3}([^_]+)_{1,3}`, str, "$1")

	// Remove code blocks
	str = mustReplace("```[^`]*```", str, "")
	str = mustReplace("`([^`]+)`", str, "$1")

	// Remove links but keep text
	str = mustReplace(`\[([^\]]+)\]\([^)]+\)`, str, "$1")

	// Remove images
	str = mustReplace(`!\[([^\]]*)\]\([^)]+\)`, str, "$1")

	// Remove headers
	str = mustReplace(`^#{1,6}\s+`, str, "")

	// Remove blockquotes
	str = mustReplace(`^>\s+`, str, "")

	// Remove horizontal rules
	str = mustReplace(`^[-*_]{3,}$`, str, "")

	return strings.TrimSpace(str)
}

// Markdown converts a small subset of inline Markdown to HTML safely.
//
// Strategy:
//  1. Extract [text](url) link constructs from the raw input into opaque
//     placeholder tokens. The URL is validated against the raw input (so
//     scheme allowlisting still sees javascript:, data:, etc.) and either
//     stored as a rendered <a> tag or replaced with escaped link text.
//     Inline markdown (bold, italic, code) inside the link label is
//     rendered before the placeholder is stored.
//  2. HTML-escape the entire remaining string. Any raw HTML in the source
//     (for example <script> or <img onerror=...>) becomes inert text.
//  3. Run the markdown regex passes against the escaped string. The opening
//     tokens for headers, bold, italic, and code are ASCII (#, *, _, `) so
//     escaping does not change them and the patterns still match. The tags
//     we emit (<h1>, <strong>, ...) are literal strings owned by this
//     function, so they pass through as real HTML.
//  4. Substitute link placeholders back with their pre-rendered markup.
//
// Link hrefs are restricted to an allowlist (http, https, mailto, plus
// relative paths and fragments). For full CommonMark support, use a real
// markdown library.
func Markdown(str string) string {
	// Step 1: pull out links into tokens against the raw input.
	str, tokens := extractMarkdownLinks(str)

	// Step 2: escape everything else so stray HTML cannot survive.
	str = html.EscapeString(str)

	// Step 3: run markdown passes. The capture groups are now pre-escaped,
	// so we substitute them in directly without an extra escape step.
	str = replaceCapture(`^### (.+)$`, str, "<h3>", "</h3>")
	str = replaceCapture(`^## (.+)$`, str, "<h2>", "</h2>")
	str = replaceCapture(`^# (.+)$`, str, "<h1>", "</h1>")

	str = applyInlineMarkdown(str)

	// Step 4: restore links. Tokens were generated with ASCII only so
	// html.EscapeString does not touch them.
	for token, rendered := range tokens {
		str = strings.ReplaceAll(str, token, rendered)
	}

	return str
}

// applyInlineMarkdown runs the inline (non-block) markdown passes against
// an already-escaped subject. Used both for the main body and for link
// label content, so [**docs**](url) renders <strong>docs</strong> inside
// the anchor.
//
// The subject must already be HTML-escaped before calling. Inline tags
// inserted here (<strong>, <em>, <code>) are literal constants owned by
// this package.
func applyInlineMarkdown(subject string) string {
	subject = replaceCapture(`\*\*([^*]+)\*\*`, subject, "<strong>", "</strong>")
	subject = replaceCapture(`__([^_]+)__`, subject, "<strong>", "</strong>")
	subject = replaceCapture(`\*([^*]+)\*`, subject, "<em>", "</em>")
	subject = replaceCapture(`_([^_]+)_`, subject, "<em>", "</em>")
	subject = replaceCapture("`([^`]+)`", subject, "<code>", "</code>")
	return subject
}

// replaceCapture wraps the first capture group of pattern in open/close.
// Callers must guarantee the capture content is already safe for HTML
// insertion (for example, the whole subject was pre-escaped). The pattern
// itself and the open/close tags are framework-owned constants.
func replaceCapture(pattern, subject, open, close string) string {
	re := getRegex(pattern)
	return re.ReplaceAllStringFunc(subject, func(match string) string {
		groups := re.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		return open + groups[1] + close
	})
}

// markdownLinkRE matches a Markdown link "[text](url)". The url group stops
// at the first close paren, matching the simple converter we are replacing.
var markdownLinkRE = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// extractMarkdownLinks finds every [text](url) in s and replaces it with an
// opaque ASCII token. It returns the rewritten string and a map of token to
// pre-rendered HTML. Tokens are constructed so html.EscapeString does not
// mutate them and so they cannot collide with input or other tokens.
func extractMarkdownLinks(s string) (string, map[string]string) {
	tokens := map[string]string{}
	idx := 0
	out := markdownLinkRE.ReplaceAllStringFunc(s, func(match string) string {
		groups := markdownLinkRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		text := groups[1]
		url := strings.TrimSpace(groups[2])

		// Render label content: escape first (so any raw HTML in the
		// label becomes inert), then apply inline markdown passes against
		// the escaped form. The tags emitted by applyInlineMarkdown are
		// trusted literal constants.
		label := applyInlineMarkdown(html.EscapeString(text))

		var rendered string
		if !isSafeMarkdownURL(url) {
			// Render the link text only. This neutralises javascript:,
			// data:, vbscript:, and anything else not in the allowlist.
			rendered = label
		} else {
			rendered = `<a href="` + html.EscapeString(url) + `">` + label + `</a>`
		}

		// Token uses only ASCII letters and digits so html.EscapeString
		// leaves it untouched. The prefix/suffix are unlikely to appear in
		// real input; if a collision happened (attacker crafts the exact
		// token literal in source) the worst case is that their literal
		// gets replaced with the rendered HTML, not an XSS.
		token := markdownLinkToken(idx)
		idx++
		tokens[token] = rendered
		return token
	})
	return out, tokens
}

// markdownLinkToken builds a placeholder token for a Markdown link. It is
// pure ASCII letters and digits so HTML escaping leaves it intact.
func markdownLinkToken(i int) string {
	return "xVELOCITYMDLINKx" + itoaBase36(i) + "xENDx"
}

// itoaBase36 formats i in base 36 using lowercase letters. Avoids importing
// strconv just for this and keeps tokens ASCII-only.
func itoaBase36(i int) string {
	if i == 0 {
		return "0"
	}
	const alpha = "0123456789abcdefghijklmnopqrstuvwxyz"
	var buf [16]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = alpha[i%36]
		i /= 36
	}
	return string(buf[pos:])
}

// isSafeMarkdownURL reports whether url is acceptable as a link target.
// Allowed: http://, https://, mailto:, fragments (#...), relative paths
// (/..., ./..., ../...). Everything else (including javascript:, data:,
// vbscript:, file:) is rejected.
//
// We also reject any URL containing characters that should never appear in
// a real URL (quotes, angle brackets, whitespace, control bytes). Those
// would not enable injection thanks to HTML escaping, but they are a strong
// signal of an attacker probing for attribute breakout and rejecting them
// keeps the rendered HTML clean.
func isSafeMarkdownURL(url string) bool {
	if url == "" {
		return false
	}
	for _, r := range url {
		if r <= 0x20 || r == '"' || r == '\'' || r == '<' || r == '>' || r == '`' || r == 0x7f {
			return false
		}
	}
	// Fragment-only and relative paths are safe.
	if strings.HasPrefix(url, "#") ||
		strings.HasPrefix(url, "/") ||
		strings.HasPrefix(url, "./") ||
		strings.HasPrefix(url, "../") {
		return true
	}
	// Check for a scheme. If there is no colon before any path separator,
	// it is a relative URL with no scheme: safe.
	colon := strings.Index(url, ":")
	if colon == -1 {
		return true
	}
	slash := strings.Index(url, "/")
	if slash != -1 && slash < colon {
		// Path appears before colon, so there is no scheme.
		return true
	}
	scheme := strings.ToLower(url[:colon])
	switch scheme {
	case "http", "https", "mailto":
		return true
	}
	return false
}

// SquishFast removes all extraneous white space from a string quickly.
func SquishFast(str string) string {
	// Replace all whitespace sequences with a single space
	return strings.Join(strings.Fields(str), " ")
}

// Squish removes all extraneous white space from a string including unicode spaces.
func Squish(str string) string {
	// Replace various unicode spaces with regular space
	str = strings.ReplaceAll(str, "\u00A0", " ") // Non-breaking space
	str = strings.ReplaceAll(str, "\u1680", " ") // Ogham space mark
	str = strings.ReplaceAll(str, "\u2000", " ") // En quad
	str = strings.ReplaceAll(str, "\u2001", " ") // Em quad
	str = strings.ReplaceAll(str, "\u2002", " ") // En space
	str = strings.ReplaceAll(str, "\u2003", " ") // Em space
	str = strings.ReplaceAll(str, "\u2004", " ") // Three-per-em space
	str = strings.ReplaceAll(str, "\u2005", " ") // Four-per-em space
	str = strings.ReplaceAll(str, "\u2006", " ") // Six-per-em space
	str = strings.ReplaceAll(str, "\u2007", " ") // Figure space
	str = strings.ReplaceAll(str, "\u2008", " ") // Punctuation space
	str = strings.ReplaceAll(str, "\u2009", " ") // Thin space
	str = strings.ReplaceAll(str, "\u200A", " ") // Hair space
	str = strings.ReplaceAll(str, "\u202F", " ") // Narrow no-break space
	str = strings.ReplaceAll(str, "\u205F", " ") // Medium mathematical space
	str = strings.ReplaceAll(str, "\u3000", " ") // Ideographic space

	return SquishFast(str)
}
