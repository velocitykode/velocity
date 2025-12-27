package str

import (
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
		result += strings.Title(strings.ToLower(words[i]))
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
		result += strings.Title(strings.ToLower(word))
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

// Markdown converts inline Markdown to HTML.
func Markdown(str string) string {
	// Basic Markdown to HTML conversion
	// For a complete implementation, a proper Markdown library should be used

	// Headers
	str = mustReplace(`^### (.+)$`, str, "<h3>$1</h3>")
	str = mustReplace(`^## (.+)$`, str, "<h2>$1</h2>")
	str = mustReplace(`^# (.+)$`, str, "<h1>$1</h1>")

	// Bold
	str = mustReplace(`\*\*([^*]+)\*\*`, str, "<strong>$1</strong>")
	str = mustReplace(`__([^_]+)__`, str, "<strong>$1</strong>")

	// Italic
	str = mustReplace(`\*([^*]+)\*`, str, "<em>$1</em>")
	str = mustReplace(`_([^_]+)_`, str, "<em>$1</em>")

	// Code
	str = mustReplace("`([^`]+)`", str, "<code>$1</code>")

	// Links
	str = mustReplace(`\[([^\]]+)\]\(([^)]+)\)`, str, `<a href="$2">$1</a>`)

	return str
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
