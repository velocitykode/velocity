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

// Markdown converts a small subset of inline Markdown to HTML. All captured
// content is HTML-escaped before substitution, so user input cannot inject
// tags or attributes. Link hrefs are restricted to a safe URI allowlist
// (http, https, mailto, plus relative paths and fragments); anything else
// renders as plain text.
//
// This is intentionally a thin converter for safe rendering of trusted-ish
// markdown content. For full CommonMark support, use a dedicated library.
func Markdown(str string) string {
	// Headers. Match the captured group then re-render with escaping.
	str = replaceWithEscape(`^### (.+)$`, str, "<h3>", "</h3>")
	str = replaceWithEscape(`^## (.+)$`, str, "<h2>", "</h2>")
	str = replaceWithEscape(`^# (.+)$`, str, "<h1>", "</h1>")

	// Bold
	str = replaceWithEscape(`\*\*([^*]+)\*\*`, str, "<strong>", "</strong>")
	str = replaceWithEscape(`__([^_]+)__`, str, "<strong>", "</strong>")

	// Italic
	str = replaceWithEscape(`\*([^*]+)\*`, str, "<em>", "</em>")
	str = replaceWithEscape(`_([^_]+)_`, str, "<em>", "</em>")

	// Code
	str = replaceWithEscape("`([^`]+)`", str, "<code>", "</code>")

	// Links. Replace by hand so we can escape both the text and the URL,
	// and reject hrefs whose scheme is not in the allowlist.
	str = renderMarkdownLinks(str)

	return str
}

// replaceWithEscape compiles pattern (must contain exactly one capture
// group), and replaces each match by wrapping the HTML-escaped capture
// between open and close. Escaping prevents tag and attribute injection from
// the captured text.
func replaceWithEscape(pattern, subject, open, close string) string {
	re := getRegex(pattern)
	return re.ReplaceAllStringFunc(subject, func(match string) string {
		// FindStringSubmatch on the already-matched string gives us the
		// capture group without rescanning the whole subject.
		groups := re.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		return open + html.EscapeString(groups[1]) + close
	})
}

// markdownLinkRE matches a Markdown link "[text](url)". The url group stops
// at the first close paren, matching the simple converter we are replacing.
var markdownLinkRE = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// renderMarkdownLinks converts each [text](url) to <a href="url">text</a>
// with HTML escaping on both fields. Disallowed schemes render as plain
// text.
func renderMarkdownLinks(s string) string {
	return markdownLinkRE.ReplaceAllStringFunc(s, func(match string) string {
		groups := markdownLinkRE.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		text := groups[1]
		url := strings.TrimSpace(groups[2])

		if !isSafeMarkdownURL(url) {
			// Render the link text only. This neutralises javascript:,
			// data:, vbscript:, and anything else not in the allowlist.
			return html.EscapeString(text)
		}
		// html.EscapeString escapes & " < > ' in attributes too, which is
		// what we want for href values inside double quotes.
		return `<a href="` + html.EscapeString(url) + `">` + html.EscapeString(text) + `</a>`
	})
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
