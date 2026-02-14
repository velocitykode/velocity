package str

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Stringable provides a fluent interface for string manipulation.
type Stringable struct {
	value string
}

// Of creates a new Stringable instance with the given value.
func Of(value interface{}) *Stringable {
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	case fmt.Stringer:
		str = v.String()
	default:
		str = fmt.Sprintf("%v", v)
	}
	return &Stringable{value: str}
}

// String returns the underlying string value.
func (s *Stringable) String() string {
	return s.value
}

// ToString is an alias for String.
func (s *Stringable) ToString() string {
	return s.value
}

// After returns the substring after the first occurrence of the given value.
func (s *Stringable) After(search string) *Stringable {
	s.value = After(s.value, search)
	return s
}

// AfterLast returns the substring after the last occurrence of the given value.
func (s *Stringable) AfterLast(search string) *Stringable {
	s.value = AfterLast(s.value, search)
	return s
}

// Append appends the given values to the string.
func (s *Stringable) Append(values ...string) *Stringable {
	s.value += strings.Join(values, "")
	return s
}

// Ascii transliterates the string to ASCII.
func (s *Stringable) Ascii() *Stringable {
	s.value = Ascii(s.value)
	return s
}

// Basename returns the trailing name component of a path.
func (s *Stringable) Basename(suffix ...string) *Stringable {
	lastSlash := strings.LastIndex(s.value, "/")
	if lastSlash == -1 {
		lastSlash = strings.LastIndex(s.value, "\\")
	}

	if lastSlash != -1 {
		s.value = s.value[lastSlash+1:]
	}

	if len(suffix) > 0 && strings.HasSuffix(s.value, suffix[0]) {
		s.value = s.value[:len(s.value)-len(suffix[0])]
	}

	return s
}

// Before returns the substring before the first occurrence of the given value.
func (s *Stringable) Before(search string) *Stringable {
	s.value = Before(s.value, search)
	return s
}

// BeforeLast returns the substring before the last occurrence of the given value.
func (s *Stringable) BeforeLast(search string) *Stringable {
	s.value = BeforeLast(s.value, search)
	return s
}

// Between returns the substring between two strings.
func (s *Stringable) Between(from, to string) *Stringable {
	s.value = Between(s.value, from, to)
	return s
}

// BetweenFirst returns the smallest possible substring between two strings.
func (s *Stringable) BetweenFirst(from, to string) *Stringable {
	s.value = BetweenFirst(s.value, from, to)
	return s
}

// Camel converts the string to camelCase.
func (s *Stringable) Camel() *Stringable {
	s.value = Camel(s.value)
	return s
}

// CharAt returns the character at the given index.
func (s *Stringable) CharAt(index int) string {
	runes := []rune(s.value)
	if index < 0 || index >= len(runes) {
		return ""
	}
	return string(runes[index])
}

// ClassBasename returns the class basename of the class path.
func (s *Stringable) ClassBasename() *Stringable {
	lastDot := strings.LastIndex(s.value, ".")
	if lastDot != -1 {
		s.value = s.value[lastDot+1:]
	}
	return s
}

// Contains checks if the string contains the given value(s).
func (s *Stringable) Contains(needles ...string) bool {
	return Contains(s.value, needles...)
}

// ContainsAll checks if the string contains all of the given values.
func (s *Stringable) ContainsAll(needles []string) bool {
	return ContainsAll(s.value, needles)
}

// Dirname returns the directory name from a path.
func (s *Stringable) Dirname(levels ...int) *Stringable {
	level := 1
	if len(levels) > 0 {
		level = levels[0]
	}

	for i := 0; i < level; i++ {
		lastSlash := strings.LastIndex(s.value, "/")
		if lastSlash == -1 {
			lastSlash = strings.LastIndex(s.value, "\\")
		}
		if lastSlash != -1 {
			s.value = s.value[:lastSlash]
		}
	}

	return s
}

// EndsWith checks if the string ends with any of the given values.
func (s *Stringable) EndsWith(needles ...string) bool {
	return EndsWith(s.value, needles...)
}

// Exactly checks if the string exactly matches the given value.
func (s *Stringable) Exactly(value string) bool {
	return Exactly(s.value, value)
}

// Excerpt extracts an excerpt from text that matches the given phrase.
func (s *Stringable) Excerpt(phrase string, options ...ExcerptOptions) *Stringable {
	s.value = Excerpt(s.value, phrase, options...)
	return s
}

// Explode splits the string by the given delimiter.
func (s *Stringable) Explode(delimiter string, limit ...int) []string {
	if len(limit) > 0 {
		return strings.SplitN(s.value, delimiter, limit[0])
	}
	return strings.Split(s.value, delimiter)
}

// Finish ensures the string ends with the given value.
func (s *Stringable) Finish(cap string) *Stringable {
	s.value = Finish(s.value, cap)
	return s
}

// Headline converts the string to a human-readable headline.
func (s *Stringable) Headline() *Stringable {
	s.value = Headline(s.value)
	return s
}

// InlineMarkdown removes all Markdown formatting from the string.
func (s *Stringable) InlineMarkdown() *Stringable {
	s.value = InlineMarkdown(s.value)
	return s
}

// Is checks if the string matches the given pattern.
func (s *Stringable) Is(pattern string) bool {
	return Is(pattern, s.value)
}

// IsAscii checks if the string is 7-bit ASCII.
func (s *Stringable) IsAscii() bool {
	return IsAscii(s.value)
}

// IsEmpty checks if the string is empty.
func (s *Stringable) IsEmpty() bool {
	return s.value == ""
}

// IsNotEmpty checks if the string is not empty.
func (s *Stringable) IsNotEmpty() bool {
	return s.value != ""
}

// IsJson checks if the string is valid JSON.
func (s *Stringable) IsJson() bool {
	return IsJson(s.value)
}

// IsUlid checks if the string is a valid ULID.
func (s *Stringable) IsUlid() bool {
	return IsUlid(s.value)
}

// IsUrl checks if the string is a valid URL.
func (s *Stringable) IsUrl() bool {
	return IsUrl(s.value)
}

// IsUuid checks if the string is a valid UUID.
func (s *Stringable) IsUuid() bool {
	return IsUuid(s.value)
}

// Kebab converts the string to kebab-case.
func (s *Stringable) Kebab() *Stringable {
	s.value = Kebab(s.value)
	return s
}

// LcFirst makes the string's first character lowercase.
func (s *Stringable) LcFirst() *Stringable {
	if s.value == "" {
		return s
	}
	runes := []rune(s.value)
	runes[0] = unicode.ToLower(runes[0])
	s.value = string(runes)
	return s
}

// Length returns the length of the string.
func (s *Stringable) Length() int {
	return Length(s.value)
}

// Limit limits the number of characters in the string.
func (s *Stringable) Limit(limit int, end ...string) *Stringable {
	s.value = Limit(s.value, limit, end...)
	return s
}

// Lower converts the string to lowercase.
func (s *Stringable) Lower() *Stringable {
	s.value = Lower(s.value)
	return s
}

// Ltrim trims the left side of the string.
func (s *Stringable) Ltrim(characters ...string) *Stringable {
	s.value = Ltrim(s.value, characters...)
	return s
}

// Markdown converts inline Markdown to HTML.
func (s *Stringable) Markdown() *Stringable {
	s.value = Markdown(s.value)
	return s
}

// Mask masks a portion of the string with a repeated character.
func (s *Stringable) Mask(character rune, index int, length ...int) *Stringable {
	s.value = Mask(s.value, character, index, length...)
	return s
}

// Match performs a pattern match on the string.
func (s *Stringable) Match(pattern string) bool {
	return Match(pattern, s.value)
}

// MatchAll performs a global pattern match on the string.
func (s *Stringable) MatchAll(pattern string) [][]string {
	return MatchAll(pattern, s.value)
}

// NewLine appends a newline to the string.
func (s *Stringable) NewLine(count ...int) *Stringable {
	n := 1
	if len(count) > 0 {
		n = count[0]
	}
	s.value += strings.Repeat("\n", n)
	return s
}

// PadBoth pads both sides of the string.
func (s *Stringable) PadBoth(length int, pad ...string) *Stringable {
	s.value = PadBoth(s.value, length, pad...)
	return s
}

// PadLeft pads the left side of the string.
func (s *Stringable) PadLeft(length int, pad ...string) *Stringable {
	s.value = PadLeft(s.value, length, pad...)
	return s
}

// PadRight pads the right side of the string.
func (s *Stringable) PadRight(length int, pad ...string) *Stringable {
	s.value = PadRight(s.value, length, pad...)
	return s
}

// Pipe passes the string to the given callback and returns the result.
func (s *Stringable) Pipe(callback func(string) interface{}) interface{} {
	return callback(s.value)
}

// Plural returns the plural form of a word.
func (s *Stringable) Plural(count ...float64) *Stringable {
	s.value = Plural(s.value, count...)
	return s
}

// Position finds the position of the first occurrence of a substring.
func (s *Stringable) Position(needle string) int {
	return Position(s.value, needle)
}

// Prepend prepends the given values to the string.
func (s *Stringable) Prepend(values ...string) *Stringable {
	s.value = strings.Join(values, "") + s.value
	return s
}

// Remove removes the given value(s) from the string.
func (s *Stringable) Remove(values ...string) *Stringable {
	for _, value := range values {
		s.value = strings.ReplaceAll(s.value, value, "")
	}
	return s
}

// Repeat repeats the string.
func (s *Stringable) Repeat(times int) *Stringable {
	s.value = Repeat(s.value, times)
	return s
}

// Replace replaces all occurrences of the search string with the replacement string.
func (s *Stringable) Replace(search, replace string) *Stringable {
	s.value = Replace(search, replace, s.value)
	return s
}

// ReplaceArray replaces the occurrences of search array with replace array.
func (s *Stringable) ReplaceArray(search, replace []string) *Stringable {
	s.value = ReplaceArray(search, replace, s.value)
	return s
}

// ReplaceFirst replaces the first occurrence of the search string.
func (s *Stringable) ReplaceFirst(search, replace string) *Stringable {
	s.value = ReplaceFirst(search, replace, s.value)
	return s
}

// ReplaceLast replaces the last occurrence of the search string.
func (s *Stringable) ReplaceLast(search, replace string) *Stringable {
	s.value = ReplaceLast(search, replace, s.value)
	return s
}

// ReplaceMatches replaces matches of the pattern with the replacement.
func (s *Stringable) ReplaceMatches(pattern, replace string) *Stringable {
	re := regexp.MustCompile(pattern)
	s.value = re.ReplaceAllString(s.value, replace)
	return s
}

// Reverse reverses the string.
func (s *Stringable) Reverse() *Stringable {
	s.value = Reverse(s.value)
	return s
}

// Rtrim trims the right side of the string.
func (s *Stringable) Rtrim(characters ...string) *Stringable {
	s.value = Rtrim(s.value, characters...)
	return s
}

// Scan parses the string according to the given format.
func (s *Stringable) Scan(format string, args ...interface{}) (int, error) {
	return fmt.Sscanf(s.value, format, args...)
}

// Singular returns the singular form of a word.
func (s *Stringable) Singular() *Stringable {
	s.value = Singular(s.value)
	return s
}

// Slug generates a URL-friendly "slug" from the string.
func (s *Stringable) Slug(separator ...string) *Stringable {
	s.value = Slug(s.value, separator...)
	return s
}

// Snake converts the string to snake_case.
func (s *Stringable) Snake() *Stringable {
	s.value = Snake(s.value)
	return s
}

// Split splits the string by the given delimiter.
func (s *Stringable) Split(delimiter string) []string {
	return strings.Split(s.value, delimiter)
}

// Squish removes all extraneous white space from the string.
func (s *Stringable) Squish() *Stringable {
	s.value = Squish(s.value)
	return s
}

// Start ensures the string starts with the given value.
func (s *Stringable) Start(prefix string) *Stringable {
	s.value = Start(s.value, prefix)
	return s
}

// StartsWith checks if the string starts with any of the given values.
func (s *Stringable) StartsWith(needles ...string) bool {
	return StartsWith(s.value, needles...)
}

// Studly converts the string to StudlyCase.
func (s *Stringable) Studly() *Stringable {
	s.value = Studly(s.value)
	return s
}

// Substr returns a substring starting at the given position.
func (s *Stringable) Substr(start int, length ...int) *Stringable {
	s.value = Substr(s.value, start, length...)
	return s
}

// SubstrCount counts the occurrences of a substring.
func (s *Stringable) SubstrCount(needle string) int {
	return SubstrCount(s.value, needle)
}

// SubstrReplace replaces text within a portion of the string.
func (s *Stringable) SubstrReplace(replace string, offset int, length ...int) *Stringable {
	s.value = SubstrReplace(s.value, replace, offset, length...)
	return s
}

// Swap replaces multiple values in the string using a map.
func (s *Stringable) Swap(pairs map[string]string) *Stringable {
	s.value = Swap(pairs, s.value)
	return s
}

// Take returns the first or last n characters of the string.
func (s *Stringable) Take(limit int) *Stringable {
	s.value = Take(s.value, limit)
	return s
}

// Tap passes the string to the given callback and returns the Stringable.
func (s *Stringable) Tap(callback func(*Stringable)) *Stringable {
	callback(s)
	return s
}

// Test checks if the string matches the given regular expression.
func (s *Stringable) Test(pattern string) bool {
	return Test(pattern, s.value)
}

// Title converts the string to title case.
func (s *Stringable) Title() *Stringable {
	s.value = Title(s.value)
	return s
}

// Trim trims the string of the given characters.
func (s *Stringable) Trim(characters ...string) *Stringable {
	s.value = Trim(s.value, characters...)
	return s
}

// UcFirst makes the string's first character uppercase.
func (s *Stringable) UcFirst() *Stringable {
	s.value = Ucfirst(s.value)
	return s
}

// UcSplit splits the string by uppercase characters.
func (s *Stringable) UcSplit() []string {
	return Ucsplit(s.value)
}

// Unless applies the callback if the given condition is false.
func (s *Stringable) Unless(condition bool, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	if !condition {
		return callback(s)
	}
	if len(defaultCallback) > 0 {
		return defaultCallback[0](s)
	}
	return s
}

// Upper converts the string to uppercase.
func (s *Stringable) Upper() *Stringable {
	s.value = Upper(s.value)
	return s
}

// When applies the callback if the given condition is true.
func (s *Stringable) When(condition bool, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	if condition {
		return callback(s)
	}
	if len(defaultCallback) > 0 {
		return defaultCallback[0](s)
	}
	return s
}

// WhenContains applies the callback if the string contains the given value.
func (s *Stringable) WhenContains(needle string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(strings.Contains(s.value, needle), callback, defaultCallback...)
}

// WhenContainsAll applies the callback if the string contains all given values.
func (s *Stringable) WhenContainsAll(needles []string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(ContainsAll(s.value, needles), callback, defaultCallback...)
}

// WhenEmpty applies the callback if the string is empty.
func (s *Stringable) WhenEmpty(callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(s.value == "", callback, defaultCallback...)
}

// WhenNotEmpty applies the callback if the string is not empty.
func (s *Stringable) WhenNotEmpty(callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(s.value != "", callback, defaultCallback...)
}

// WhenStartsWith applies the callback if the string starts with the given value.
func (s *Stringable) WhenStartsWith(needles []string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	starts := false
	for _, needle := range needles {
		if strings.HasPrefix(s.value, needle) {
			starts = true
			break
		}
	}
	return s.When(starts, callback, defaultCallback...)
}

// WhenEndsWith applies the callback if the string ends with the given value.
func (s *Stringable) WhenEndsWith(needles []string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	ends := false
	for _, needle := range needles {
		if strings.HasSuffix(s.value, needle) {
			ends = true
			break
		}
	}
	return s.When(ends, callback, defaultCallback...)
}

// WhenExactly applies the callback if the string exactly matches the given value.
func (s *Stringable) WhenExactly(value string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(s.value == value, callback, defaultCallback...)
}

// WhenNotExactly applies the callback if the string does not exactly match the given value.
func (s *Stringable) WhenNotExactly(value string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(s.value != value, callback, defaultCallback...)
}

// WhenIs applies the callback if the string matches the given pattern.
func (s *Stringable) WhenIs(pattern string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(Is(pattern, s.value), callback, defaultCallback...)
}

// WhenIsAscii applies the callback if the string is ASCII.
func (s *Stringable) WhenIsAscii(callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(IsAscii(s.value), callback, defaultCallback...)
}

// WhenIsUlid applies the callback if the string is a valid ULID.
func (s *Stringable) WhenIsUlid(callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(IsUlid(s.value), callback, defaultCallback...)
}

// WhenIsUuid applies the callback if the string is a valid UUID.
func (s *Stringable) WhenIsUuid(callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(IsUuid(s.value), callback, defaultCallback...)
}

// WhenTest applies the callback if the string matches the given regular expression.
func (s *Stringable) WhenTest(pattern string, callback func(*Stringable) *Stringable, defaultCallback ...func(*Stringable) *Stringable) *Stringable {
	return s.When(Test(pattern, s.value), callback, defaultCallback...)
}

// WordCount counts the words in the string.
func (s *Stringable) WordCount() int {
	return WordCount(s.value)
}

// Words limits the number of words in the string.
func (s *Stringable) Words(words int, end ...string) *Stringable {
	s.value = Words(s.value, words, end...)
	return s
}

// WordWrap wraps the string at the given length.
func (s *Stringable) WordWrap(width int, breakStr ...string) *Stringable {
	s.value = WordWrap(s.value, width, breakStr...)
	return s
}
