package str

import (
	"regexp"
	"strings"

	"github.com/velocitykode/velocity/internal/inflect"
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
	return inflect.Kebab(value)
}

// Snake converts the string to snake_case.
func Snake(value string) string {
	return inflect.Snake(value)
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
