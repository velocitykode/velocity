package inflect

import (
	"strings"
	"unicode"
)

// Plural returns the plural form of a word using the common English rules.
// When count is given and equals 1 the word is returned unchanged.
func Plural(value string, count ...float64) string {
	if len(count) > 0 && count[0] == 1 {
		return value
	}

	if len(value) >= 2 && strings.HasSuffix(value, "y") && !isVowel(rune(value[len(value)-2])) {
		return value[:len(value)-1] + "ies"
	}
	if strings.HasSuffix(value, "s") || strings.HasSuffix(value, "x") ||
		strings.HasSuffix(value, "ch") || strings.HasSuffix(value, "sh") {
		return value + "es"
	}
	if strings.HasSuffix(value, "f") {
		return value[:len(value)-1] + "ves"
	}
	if strings.HasSuffix(value, "fe") {
		return value[:len(value)-2] + "ves"
	}

	return value + "s"
}

// Singular returns the singular form of a word using the common English
// rules.
func Singular(value string) string {
	if strings.HasSuffix(value, "ies") {
		return value[:len(value)-3] + "y"
	}
	if strings.HasSuffix(value, "ves") {
		if len(value) > 4 && value[len(value)-4] == 'l' {
			return value[:len(value)-3] + "f"
		}
		return value[:len(value)-3] + "fe"
	}
	if strings.HasSuffix(value, "es") {
		if strings.HasSuffix(value[:len(value)-2], "s") ||
			strings.HasSuffix(value[:len(value)-2], "x") ||
			strings.HasSuffix(value[:len(value)-2], "ch") ||
			strings.HasSuffix(value[:len(value)-2], "sh") {
			return value[:len(value)-2]
		}
	}
	if strings.HasSuffix(value, "s") && !strings.HasSuffix(value, "ss") {
		return value[:len(value)-1]
	}

	return value
}

// Snake converts the value to snake_case.
func Snake(value string) string {
	return Delimited(value, '_')
}

// Kebab converts the value to kebab-case.
func Kebab(value string) string {
	return Delimited(value, '-')
}

// Delimited lower-cases the value and joins its words with delimiter. Words
// are split on underscores, hyphens, spaces, dots and camelCase boundaries
// (acronyms stay together); runs of delimiters collapse and leading or
// trailing delimiters are trimmed.
func Delimited(s string, delimiter rune) string {
	if s == "" {
		return s
	}

	s = strings.ReplaceAll(s, "_", string(delimiter))
	s = strings.ReplaceAll(s, "-", string(delimiter))
	s = strings.ReplaceAll(s, " ", string(delimiter))
	s = strings.ReplaceAll(s, ".", string(delimiter))

	runes := []rune(s)
	result := make([]rune, 0, len(runes))

	// emitDelimiter appends a delimiter only when the previous emitted rune
	// is not already a delimiter, collapsing runs in the single pass.
	emitDelimiter := func() {
		if len(result) > 0 && result[len(result)-1] != delimiter {
			result = append(result, delimiter)
		}
	}

	for i := 0; i < len(runes); i++ {
		char := runes[i]
		switch {
		case unicode.IsUpper(char):
			if i > 0 && runes[i-1] != delimiter {
				if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					// Start of a new word.
					emitDelimiter()
				} else if unicode.IsLower(runes[i-1]) {
					// Previous rune was lowercase, so this starts a word.
					emitDelimiter()
				}
			}
			result = append(result, unicode.ToLower(char))
		case char == delimiter:
			emitDelimiter()
		default:
			result = append(result, char)
		}
	}

	return strings.Trim(string(result), string(delimiter))
}

func isVowel(r rune) bool {
	return strings.ContainsRune("aeiouAEIOU", r)
}
