package str

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var titleCaser = cases.Title(language.Und)

// After returns the substring after the first occurrence of the given value.
// If the value doesn't exist, returns the entire string.
func After(subject, search string) string {
	if search == "" {
		return subject
	}
	pos := strings.Index(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[pos+len(search):]
}

// AfterLast returns the substring after the last occurrence of the given value.
// If the value doesn't exist, returns the entire string.
func AfterLast(subject, search string) string {
	if search == "" {
		return subject
	}
	pos := strings.LastIndex(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[pos+len(search):]
}

// Before returns the substring before the first occurrence of the given value.
// If the value doesn't exist, returns the entire string.
func Before(subject, search string) string {
	if search == "" {
		return subject
	}
	pos := strings.Index(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[:pos]
}

// BeforeLast returns the substring before the last occurrence of the given value.
// If the value doesn't exist, returns the entire string.
func BeforeLast(subject, search string) string {
	if search == "" {
		return subject
	}
	pos := strings.LastIndex(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[:pos]
}

// Between returns the substring between two strings.
func Between(subject, from, to string) string {
	if from == "" || to == "" {
		return subject
	}

	startPos := strings.Index(subject, from)
	if startPos == -1 {
		return subject
	}

	startPos += len(from)
	endPos := strings.Index(subject[startPos:], to)
	if endPos == -1 {
		return subject
	}

	return subject[startPos : startPos+endPos]
}

// BetweenFirst returns the smallest possible substring between two strings.
func BetweenFirst(subject, from, to string) string {
	return Between(subject, from, to)
}

// Contains checks if the string contains any of the given values. Mirrors the
// any-match semantics of StartsWith and EndsWith. Use ContainsAll to require
// every value.
func Contains(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

// ContainsAll checks if the string contains all of the given values.
func ContainsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// EndsWith checks if the string ends with any of the given values.
func EndsWith(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.HasSuffix(haystack, needle) {
			return true
		}
	}
	return false
}

// Exactly checks if the string exactly matches the given value.
func Exactly(subject, value string) bool {
	return subject == value
}

// Excerpt extracts an excerpt from text that matches the given phrase.
type ExcerptOptions struct {
	Radius   int
	Omission string
}

func Excerpt(text, phrase string, options ...ExcerptOptions) string {
	opt := ExcerptOptions{
		Radius:   100,
		Omission: "...",
	}
	if len(options) > 0 {
		opt = options[0]
	}

	// Work in runes so the radius is measured in characters and slicing
	// never splits a multi-byte rune. Lower per-rune with unicode.ToLower:
	// unlike strings.ToLower it preserves the rune COUNT (no special
	// casing), so offsets in the lowered slice map 1:1 onto the original.
	textRunes := []rune(text)
	phraseRunes := []rune(phrase)

	lowerText := make([]rune, len(textRunes))
	for i, r := range textRunes {
		lowerText[i] = unicode.ToLower(r)
	}
	lowerPhrase := make([]rune, len(phraseRunes))
	for i, r := range phraseRunes {
		lowerPhrase[i] = unicode.ToLower(r)
	}

	index := runeIndex(lowerText, lowerPhrase)
	if index == -1 {
		return ""
	}

	start := index - opt.Radius
	if start < 0 {
		start = 0
	}

	end := index + len(phraseRunes) + opt.Radius
	if end > len(textRunes) {
		end = len(textRunes)
	}

	result := string(textRunes[start:end])
	if start > 0 {
		result = opt.Omission + strings.TrimLeft(result, " ")
	}
	if end < len(textRunes) {
		result = strings.TrimRight(result, " ") + opt.Omission
	}

	return result
}

// runeIndex returns the index of the first occurrence of needle in haystack,
// comparing rune by rune, or -1 if absent. An empty needle returns 0 to match
// strings.Index semantics.
func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// Finish ensures a string ends with a given value.
func Finish(value, cap string) string {
	if strings.HasSuffix(value, cap) {
		return value
	}
	return value + cap
}

// Headline converts the string to a human-readable headline.
func Headline(value string) string {
	// Replace underscores and dashes with spaces
	value = strings.ReplaceAll(value, "_", " ")
	value = strings.ReplaceAll(value, "-", " ")

	// Split on camel case
	var result []rune
	runes := []rune(value)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && (i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
			result = append(result, ' ')
		}
		result = append(result, r)
	}

	// Title case
	return Title(string(result))
}

// Is checks if the string matches the given glob pattern. Only * and ? are
// treated as wildcards; all other characters are treated literally. Prefer
// Exactly or HasPrefix for security-sensitive allow/deny checks when possible.
func Is(pattern, value string) bool {
	ok, _ := IsSafe(pattern, value)
	return ok
}

// IsAscii checks if the string is 7-bit ASCII.
func IsAscii(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}

// IsJson checks if the string is valid JSON.
func IsJson(value string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(value), &js) == nil
}

// IsUlid checks if the string is a valid ULID.
func IsUlid(value string) bool {
	if len(value) != 26 {
		return false
	}

	// Check if all characters are valid Crockford Base32
	for _, r := range value {
		if !((r >= '0' && r <= '9') || (r >= 'A' && r <= 'H') || (r >= 'J' && r <= 'K') ||
			(r >= 'M' && r <= 'N') || (r >= 'P' && r <= 'T') || (r >= 'V' && r <= 'Z')) {
			return false
		}
	}
	return true
}

// IsUrl checks if the string is a valid URL.
func IsUrl(value string) bool {
	return mustMatch(`^(https?|ftp)://[^\s/$.?#].[^\s]*$`, value)
}

// IsUuid checks if the string is a valid UUID.
func IsUuid(value string) bool {
	return mustMatch(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, strings.ToLower(value))
}

// Length returns the length of the string (UTF-8 aware).
func Length(value string) int {
	return utf8.RuneCountInString(value)
}

// Limit limits the number of characters in a string.
func Limit(value string, limit int, end ...string) string {
	suffix := "..."
	if len(end) > 0 {
		suffix = end[0]
	}

	if limit < 0 {
		limit = 0
	}

	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}

	return string(runes[:limit]) + suffix
}

// Lower converts the string to lowercase.
func Lower(value string) string {
	return strings.ToLower(value)
}

// Words limits the number of words in a string.
func Words(value string, words int, end ...string) string {
	suffix := "..."
	if len(end) > 0 {
		suffix = end[0]
	}

	if words < 0 {
		words = 0
	}

	wordList := strings.Fields(value)
	if len(wordList) <= words {
		return value
	}

	return strings.Join(wordList[:words], " ") + suffix
}

// Mask masks a portion of a string with a repeated character.
func Mask(str string, character rune, index int, length ...int) string {
	runes := []rune(str)
	maskLength := -1

	if len(length) > 0 {
		maskLength = length[0]
	}

	// Handle negative index (from end)
	if index < 0 {
		index = len(runes) + index
	}

	// Validate index
	if index < 0 || index >= len(runes) {
		return str
	}

	// Determine mask length
	if maskLength == -1 {
		maskLength = len(runes) - index
	}

	// Apply mask
	for i := 0; i < maskLength && index+i < len(runes); i++ {
		runes[index+i] = character
	}

	return string(runes)
}

// Match performs a pattern match on the string. Returns false if the
// pattern is malformed (no panic). Use MatchSafe to surface the
// compilation error.
func Match(pattern, value string) bool {
	ok, _ := MatchSafe(pattern, value)
	return ok
}

// MatchAll performs a global pattern match on the string. Returns nil if
// the pattern is malformed (no panic). Use MatchAllSafe to surface the
// compilation error.
func MatchAll(pattern, subject string) [][]string {
	out, _ := MatchAllSafe(pattern, subject)
	return out
}

// Test checks if the string matches the given pattern. Returns false if
// the pattern is malformed (no panic). Use TestSafe to surface the
// compilation error.
func Test(pattern, value string) bool {
	return Match(pattern, value)
}

// MatchSafe performs a pattern match. Returns an error if the pattern is
// malformed instead of panicking. Always prefer this over Match when the
// pattern can come from a user or external input.
func MatchSafe(pattern, value string) (bool, error) {
	re, err := getRegexE(pattern)
	if err != nil {
		return false, err
	}
	return re.MatchString(value), nil
}

// MatchAllSafe performs a global pattern match. Returns an error if the
// pattern is malformed. Always prefer this over MatchAll when the pattern
// can come from a user or external input.
func MatchAllSafe(pattern, subject string) ([][]string, error) {
	re, err := getRegexE(pattern)
	if err != nil {
		return nil, err
	}
	return re.FindAllStringSubmatch(subject, -1), nil
}

// TestSafe is an alias for MatchSafe matching the existing Test/Match
// naming pair.
func TestSafe(pattern, value string) (bool, error) {
	return MatchSafe(pattern, value)
}

// IsSafe is like Is but returns an error if the resulting regex cannot be
// compiled. Only * and ? are treated as glob wildcards; all other characters
// are treated literally. Prefer Exactly or HasPrefix for security-sensitive
// allow/deny checks when possible.
func IsSafe(pattern, value string) (bool, error) {
	if pattern == value {
		return true, nil
	}
	p := regexp.QuoteMeta(pattern)
	p = strings.ReplaceAll(p, `\*`, ".*")
	p = strings.ReplaceAll(p, `\?`, ".")
	p = "^" + p + "$"
	return MatchSafe(p, value)
}

// padRunes builds a pad string of exactly count runes by cycling padStr's
// runes and truncating, so a multi-rune pad fills the gap to the exact width
// rather than repeating whole copies. Returns "" if count <= 0 or padStr is
// empty (the empty padStr guard also avoids an infinite cycle).
func padRunes(padStr string, count int) string {
	if count <= 0 || padStr == "" {
		return ""
	}
	padded := []rune(padStr)
	out := make([]rune, count)
	for i := 0; i < count; i++ {
		out[i] = padded[i%len(padded)]
	}
	return string(out)
}

// PadBoth pads both sides of the string to the given rune length.
func PadBoth(value string, length int, pad ...string) string {
	padStr := " "
	if len(pad) > 0 {
		padStr = pad[0]
	}

	valLen := utf8.RuneCountInString(value)
	if valLen >= length || padStr == "" {
		return value
	}

	totalPad := length - valLen
	leftPad := totalPad / 2
	rightPad := totalPad - leftPad

	return padRunes(padStr, leftPad) + value + padRunes(padStr, rightPad)
}

// PadLeft pads the left side of the string to the given rune length.
func PadLeft(value string, length int, pad ...string) string {
	padStr := " "
	if len(pad) > 0 {
		padStr = pad[0]
	}

	valLen := utf8.RuneCountInString(value)
	if valLen >= length || padStr == "" {
		return value
	}

	return padRunes(padStr, length-valLen) + value
}

// PadRight pads the right side of the string to the given rune length.
func PadRight(value string, length int, pad ...string) string {
	padStr := " "
	if len(pad) > 0 {
		padStr = pad[0]
	}

	valLen := utf8.RuneCountInString(value)
	if valLen >= length || padStr == "" {
		return value
	}

	return value + padRunes(padStr, length-valLen)
}

// Plural returns the plural form of a word.
func Plural(value string, count ...float64) string {
	// Simple English pluralization rules
	if len(count) > 0 && count[0] == 1 {
		return value
	}

	// Common patterns
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

// Position finds the position of the first occurrence of a substring.
func Position(haystack, needle string) int {
	return strings.Index(haystack, needle)
}

// randReader is the entropy source used by Random. It is a variable so tests
// can swap it for a faulty reader. Callers in production code should not
// reassign this.
//
// trace/trace.go has a parallel randReader seam. They are intentionally NOT
// shared: str.Random returns an error on entropy failure, while trace must
// never fail a request and instead emits fallback markers plus a one-shot warn.
var randReader io.Reader = rand.Reader

// randomLetters is the alphabet used by Random. 62 characters means we use
// rejection sampling against 248 (the largest multiple of 62 below 256) to
// avoid modulo bias.
const randomLetters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// Random generates a cryptographically random alphanumeric string of the
// specified length (default 16). Returns an error if the system entropy source
// fails. Uses rejection sampling to avoid modulo bias across the 62-character
// alphabet, so every letter has equal probability.
func Random(length ...int) (string, error) {
	n := 16
	if len(length) > 0 {
		n = length[0]
	}
	if n <= 0 {
		return "", nil
	}

	const alphabetLen = byte(len(randomLetters)) // 62
	// 256 mod 62 = 8, so the largest multiple of 62 that fits in a byte is 248.
	// Any byte value >= 248 is rejected to keep the distribution uniform.
	const rejectThreshold = byte(256 - (256 % int(alphabetLen))) // 248

	out := make([]byte, 0, n)
	// Pull bytes in chunks. Most will be accepted, so chunk size n is usually
	// enough. Loop reads more on heavy rejection.
	buf := make([]byte, n)
	for len(out) < n {
		if _, err := io.ReadFull(randReader, buf); err != nil {
			return "", fmt.Errorf("str.Random: read entropy: %w", err)
		}
		for _, b := range buf {
			if b >= rejectThreshold {
				continue
			}
			out = append(out, randomLetters[b%alphabetLen])
			if len(out) == n {
				break
			}
		}
	}
	return string(out), nil
}

// Repeat repeats the string.
func Repeat(str string, times int) string {
	return strings.Repeat(str, times)
}

// Replace replaces all occurrences of the search string with the replacement string.
func Replace(search, replace, subject string) string {
	return strings.ReplaceAll(subject, search, replace)
}

// ReplaceArray replaces the occurrences of search array with replace array sequentially.
func ReplaceArray(search, replace []string, subject string) string {
	for i := 0; i < len(search) && i < len(replace); i++ {
		subject = strings.ReplaceAll(subject, search[i], replace[i])
	}
	return subject
}

// ReplaceFirst replaces the first occurrence of the search string with the replacement string.
func ReplaceFirst(search, replace, subject string) string {
	pos := strings.Index(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[:pos] + replace + subject[pos+len(search):]
}

// ReplaceLast replaces the last occurrence of the search string with the replacement string.
func ReplaceLast(search, replace, subject string) string {
	pos := strings.LastIndex(subject, search)
	if pos == -1 {
		return subject
	}
	return subject[:pos] + replace + subject[pos+len(search):]
}

// Reverse reverses the string (UTF-8 aware).
func Reverse(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// Singular returns the singular form of a word.
func Singular(value string) string {
	// Simple English singularization rules
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

// Start ensures a string starts with a given value.
func Start(value, prefix string) string {
	if strings.HasPrefix(value, prefix) {
		return value
	}
	return prefix + value
}

// StartsWith checks if the string starts with any of the given values.
func StartsWith(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if strings.HasPrefix(haystack, needle) {
			return true
		}
	}
	return false
}

// Substr returns a substring starting at the given position.
func Substr(str string, start int, length ...int) string {
	runes := []rune(str)

	// Handle negative start (from end)
	if start < 0 {
		start = len(runes) + start
	}

	// Validate start
	if start < 0 || start >= len(runes) {
		return ""
	}

	// Determine length
	end := len(runes)
	if len(length) > 0 {
		end = start + length[0]
		if end > len(runes) {
			end = len(runes)
		}
	}

	return string(runes[start:end])
}

// SubstrCount counts the occurrences of a substring.
func SubstrCount(haystack, needle string) int {
	return strings.Count(haystack, needle)
}

// SubstrReplace replaces text within a portion of a string.
func SubstrReplace(str, replace string, offset int, length ...int) string {
	runes := []rune(str)

	// Handle negative offset (from end)
	if offset < 0 {
		offset = len(runes) + offset
	}

	// Validate offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(runes) {
		return str
	}

	// Determine length
	end := len(runes)
	if len(length) > 0 && length[0] >= 0 {
		end = offset + length[0]
		if end > len(runes) {
			end = len(runes)
		}
	}

	return string(runes[:offset]) + replace + string(runes[end:])
}

// Swap replaces multiple values in the string using a map.
func Swap(pairs map[string]string, subject string) string {
	for search, replace := range pairs {
		subject = strings.ReplaceAll(subject, search, replace)
	}
	return subject
}

// Take returns the first n characters of the string.
func Take(str string, limit int) string {
	if limit < 0 {
		return Substr(str, limit)
	}
	return Substr(str, 0, limit)
}

// Title converts the string to title case.
func Title(value string) string {
	return titleCaser.String(strings.ToLower(value))
}

// Trim trims the string of the given characters.
func Trim(value string, characters ...string) string {
	if len(characters) == 0 {
		return strings.TrimSpace(value)
	}
	return strings.Trim(value, characters[0])
}

// Ltrim trims the left side of the string.
func Ltrim(value string, characters ...string) string {
	if len(characters) == 0 {
		return strings.TrimLeft(value, " \t\n\r\v\f")
	}
	return strings.TrimLeft(value, characters[0])
}

// Rtrim trims the right side of the string.
func Rtrim(value string, characters ...string) string {
	if len(characters) == 0 {
		return strings.TrimRight(value, " \t\n\r\v\f")
	}
	return strings.TrimRight(value, characters[0])
}

// Ucfirst makes the string's first character uppercase.
func Ucfirst(str string) string {
	if str == "" {
		return ""
	}
	runes := []rune(str)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// Ucsplit splits the string by uppercase characters.
func Ucsplit(str string) []string {
	var result []string
	var current []rune

	runes := []rune(str)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			if len(current) > 0 {
				result = append(result, string(current))
				current = []rune{}
			}
		}
		current = append(current, r)
	}

	if len(current) > 0 {
		result = append(result, string(current))
	}

	return result
}

// Upper converts the string to uppercase.
func Upper(value string) string {
	return strings.ToUpper(value)
}

// When applies the callback if the given condition is true.
func When(condition bool, value string, callback func(string) string, defaultCallback ...func(string) string) string {
	if condition {
		return callback(value)
	}
	if len(defaultCallback) > 0 {
		return defaultCallback[0](value)
	}
	return value
}

// WhenContains applies the callback if the string contains the given value.
func WhenContains(haystack, needle string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(strings.Contains(haystack, needle), haystack, callback, defaultCallback...)
}

// WhenContainsAll applies the callback if the string contains all given values.
func WhenContainsAll(haystack string, needles []string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(ContainsAll(haystack, needles), haystack, callback, defaultCallback...)
}

// WhenEmpty applies the callback if the string is empty.
func WhenEmpty(value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(value == "", value, callback, defaultCallback...)
}

// WhenNotEmpty applies the callback if the string is not empty.
func WhenNotEmpty(value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(value != "", value, callback, defaultCallback...)
}

// WhenStartsWith applies the callback if the string starts with the given value.
func WhenStartsWith(haystack string, needles []string, callback func(string) string, defaultCallback ...func(string) string) string {
	starts := false
	for _, needle := range needles {
		if strings.HasPrefix(haystack, needle) {
			starts = true
			break
		}
	}
	return When(starts, haystack, callback, defaultCallback...)
}

// WhenEndsWith applies the callback if the string ends with the given value.
func WhenEndsWith(haystack string, needles []string, callback func(string) string, defaultCallback ...func(string) string) string {
	ends := false
	for _, needle := range needles {
		if strings.HasSuffix(haystack, needle) {
			ends = true
			break
		}
	}
	return When(ends, haystack, callback, defaultCallback...)
}

// WhenExactly applies the callback if the string exactly matches the given value.
func WhenExactly(subject, value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(subject == value, subject, callback, defaultCallback...)
}

// WhenNotExactly applies the callback if the string does not exactly match the given value.
func WhenNotExactly(subject, value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(subject != value, subject, callback, defaultCallback...)
}

// WhenIs applies the callback if the string matches the given pattern.
func WhenIs(pattern, value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(Is(pattern, value), value, callback, defaultCallback...)
}

// WhenIsAscii applies the callback if the string is ASCII.
func WhenIsAscii(value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(IsAscii(value), value, callback, defaultCallback...)
}

// WhenIsUlid applies the callback if the string is a valid ULID.
func WhenIsUlid(value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(IsUlid(value), value, callback, defaultCallback...)
}

// WhenIsUuid applies the callback if the string is a valid UUID.
func WhenIsUuid(value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(IsUuid(value), value, callback, defaultCallback...)
}

// WhenTest applies the callback if the string matches the given regular expression.
func WhenTest(pattern, value string, callback func(string) string, defaultCallback ...func(string) string) string {
	return When(Test(pattern, value), value, callback, defaultCallback...)
}

// WordCount counts the words in the string.
func WordCount(str string) int {
	return len(strings.Fields(str))
}

// WordWrap wraps the string at the given length.
func WordWrap(str string, width int, breakStr ...string) string {
	if width <= 0 {
		return str
	}

	brk := "\n"
	if len(breakStr) > 0 {
		brk = breakStr[0]
	}

	var result []string
	words := strings.Fields(str)
	var currentLine []string
	currentLength := 0

	for _, word := range words {
		wordLength := len(word)
		if currentLength > 0 && currentLength+1+wordLength > width {
			result = append(result, strings.Join(currentLine, " "))
			currentLine = []string{word}
			currentLength = wordLength
		} else {
			currentLine = append(currentLine, word)
			if currentLength > 0 {
				currentLength += 1
			}
			currentLength += wordLength
		}
	}

	if len(currentLine) > 0 {
		result = append(result, strings.Join(currentLine, " "))
	}

	return strings.Join(result, brk)
}

// Helper function to check if a rune is a vowel
func isVowel(r rune) bool {
	vowels := "aeiouAEIOU"
	return strings.ContainsRune(vowels, r)
}
