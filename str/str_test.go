package str

import (
	"strings"
	"testing"
)

func TestAfter(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		search   string
		expected string
	}{
		{"basic", "This is my name", "This is", " my name"},
		{"not found", "Hannah", "Hello", "Hannah"},
		{"empty search", "Hello", "", "Hello"},
		{"at beginning", "App\\Http\\Controllers\\Controller", "App", "\\Http\\Controllers\\Controller"},
		{"multiple occurrences", "yvette", "y", "vette"},
		{"at end", "yvette", "tte", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := After(tt.subject, tt.search)
			if result != tt.expected {
				t.Errorf("After(%q, %q) = %q; want %q", tt.subject, tt.search, result, tt.expected)
			}
		})
	}
}

func TestAfterLast(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		search   string
		expected string
	}{
		{"basic", "App\\Http\\Controllers\\Controller", "\\", "Controller"},
		{"not found", "Hannah", "Hello", "Hannah"},
		{"empty search", "Hello", "", "Hello"},
		{"multiple occurrences", "yvette", "t", "e"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := AfterLast(tt.subject, tt.search)
			if result != tt.expected {
				t.Errorf("AfterLast(%q, %q) = %q; want %q", tt.subject, tt.search, result, tt.expected)
			}
		})
	}
}

func TestBefore(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		search   string
		expected string
	}{
		{"basic", "This is my name", "my name", "This is "},
		{"not found", "Hannah", "Hello", "Hannah"},
		{"empty search", "Hello", "", "Hello"},
		{"at beginning", "App\\Http\\Controllers\\Controller", "App", ""},
		{"multiple occurrences", "yvette", "t", "yve"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Before(tt.subject, tt.search)
			if result != tt.expected {
				t.Errorf("Before(%q, %q) = %q; want %q", tt.subject, tt.search, result, tt.expected)
			}
		})
	}
}

func TestBetween(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		from     string
		to       string
		expected string
	}{
		{"basic", "This is my name", "This", "name", " is my "},
		{"brackets", "[a] bc [d]", "[", "]", "a"},
		{"not found from", "abc", "x", "c", "abc"},
		{"not found to", "abc", "a", "x", "abc"},
		{"empty markers", "abc", "", "", "abc"},
		{"multiple occurrences", "[a][b][c]", "[", "]", "a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Between(tt.subject, tt.from, tt.to)
			if result != tt.expected {
				t.Errorf("Between(%q, %q, %q) = %q; want %q", tt.subject, tt.from, tt.to, result, tt.expected)
			}
		})
	}
}

func TestCamel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"snake_case", "foo_bar", "fooBar"},
		{"kebab-case", "foo-bar", "fooBar"},
		{"StudlyCase", "FooBar", "foobar"},
		{"spaces", "foo bar", "fooBar"},
		{"mixed", "foo_bar-baz", "fooBarBaz"},
		{"single", "foo", "foo"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Camel(tt.input)
			if result != tt.expected {
				t.Errorf("Camel(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSnake(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"camelCase", "fooBar", "foo_bar"},
		{"StudlyCase", "FooBar", "foo_bar"},
		{"kebab-case", "foo-bar", "foo_bar"},
		{"spaces", "foo bar", "foo_bar"},
		{"mixed", "fooBar-baz", "foo_bar_baz"},
		{"consecutive caps", "IOError", "io_error"},
		{"single", "foo", "foo"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Snake(tt.input)
			if result != tt.expected {
				t.Errorf("Snake(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestKebab(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"camelCase", "fooBar", "foo-bar"},
		{"StudlyCase", "FooBar", "foo-bar"},
		{"snake_case", "foo_bar", "foo-bar"},
		{"spaces", "foo bar", "foo-bar"},
		{"mixed", "fooBar_baz", "foo-bar-baz"},
		{"single", "foo", "foo"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Kebab(tt.input)
			if result != tt.expected {
				t.Errorf("Kebab(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestStudly(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"snake_case", "foo_bar", "FooBar"},
		{"kebab-case", "foo-bar", "FooBar"},
		{"camelCase", "fooBar", "Foobar"},
		{"spaces", "foo bar", "FooBar"},
		{"mixed", "foo_bar-baz", "FooBarBaz"},
		{"single", "foo", "Foo"},
		{"empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Studly(tt.input)
			if result != tt.expected {
				t.Errorf("Studly(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		separator string
		expected  string
	}{
		{"basic", "Velocity 1 Framework", "-", "velocity-1-framework"},
		{"with dots", "Velocity 1.x Framework", "-", "velocity-1-x-framework"},
		{"special chars", "Velocity @ Framework!", "-", "velocity-framework"},
		{"custom separator", "Velocity Framework", "_", "velocity_framework"},
		{"multiple spaces", "Velocity  Framework", "-", "velocity-framework"},
		{"leading trailing spaces", "  Velocity Framework  ", "-", "velocity-framework"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Slug(tt.input, tt.separator)
			if result != tt.expected {
				t.Errorf("Slug(%q, %q) = %q; want %q", tt.input, tt.separator, result, tt.expected)
			}
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		needles  []string
		expected bool
	}{
		{"single found", "This is my name", []string{"my"}, true},
		{"single not found", "This is my name", []string{"foo"}, false},
		{"multiple all found", "This is my name", []string{"is", "my"}, true},
		{"multiple one missing", "This is my name", []string{"is", "foo"}, false},
		{"case sensitive", "This is my name", []string{"MY"}, false},
		{"empty needle", "This is my name", []string{""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Contains(tt.haystack, tt.needles...)
			if result != tt.expected {
				t.Errorf("Contains(%q, %v) = %v; want %v", tt.haystack, tt.needles, result, tt.expected)
			}
		})
	}
}

func TestStartsWith(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		needles  []string
		expected bool
	}{
		{"single match", "This is my name", []string{"This"}, true},
		{"multiple one match", "This is my name", []string{"That", "This"}, true},
		{"no match", "This is my name", []string{"That"}, false},
		{"case sensitive", "This is my name", []string{"this"}, false},
		{"empty needle", "This is my name", []string{""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StartsWith(tt.haystack, tt.needles...)
			if result != tt.expected {
				t.Errorf("StartsWith(%q, %v) = %v; want %v", tt.haystack, tt.needles, result, tt.expected)
			}
		})
	}
}

func TestEndsWith(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		needles  []string
		expected bool
	}{
		{"single match", "This is my name", []string{"name"}, true},
		{"multiple one match", "This is my name", []string{"foo", "name"}, true},
		{"no match", "This is my name", []string{"foo"}, false},
		{"case sensitive", "This is my name", []string{"Name"}, false},
		{"empty needle", "This is my name", []string{""}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := EndsWith(tt.haystack, tt.needles...)
			if result != tt.expected {
				t.Errorf("EndsWith(%q, %v) = %v; want %v", tt.haystack, tt.needles, result, tt.expected)
			}
		})
	}
}

func TestLimit(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		limit    int
		end      string
		expected string
	}{
		{"basic truncate", "The quick brown fox", 10, "...", "The quick ..."},
		{"no truncate needed", "Short", 10, "...", "Short"},
		{"exact limit", "12345", 5, "...", "12345"},
		{"custom ending", "The quick brown fox", 10, " >>", "The quick  >>"},
		{"utf8 aware", "你好世界测试", 3, "...", "你好世..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Limit(tt.value, tt.limit, tt.end)
			if result != tt.expected {
				t.Errorf("Limit(%q, %d, %q) = %q; want %q", tt.value, tt.limit, tt.end, result, tt.expected)
			}
		})
	}
}

func TestMask(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		character rune
		index     int
		length    int
		expected  string
	}{
		{"email partial", "taylor@example.com", '*', 6, 9, "taylor*********com"},
		{"phone partial", "1234567890", '*', 3, 4, "123****890"},
		{"from start", "secret", '*', 0, 3, "***ret"},
		{"to end", "secret", '*', 3, -1, "sec***"},
		{"negative index", "secret", '*', -3, -1, "sec***"},
		{"out of bounds", "secret", '*', 10, 3, "secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Mask(tt.input, tt.character, tt.index, tt.length)
			if result != tt.expected {
				t.Errorf("Mask(%q, %q, %d, %d) = %q; want %q", tt.input, string(tt.character), tt.index, tt.length, result, tt.expected)
			}
		})
	}
}

func TestReverse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"basic", "Hello", "olleH"},
		{"empty", "", ""},
		{"single", "a", "a"},
		{"utf8", "你好", "好你"},
		{"mixed", "Hello世界", "界世olleH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Reverse(tt.input)
			if result != tt.expected {
				t.Errorf("Reverse(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsUuid(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{"valid v4", "550e8400-e29b-41d4-a716-446655440000", true},
		{"valid uppercase", "550E8400-E29B-41D4-A716-446655440000", true},
		{"invalid format", "not-a-uuid", false},
		{"missing dashes", "550e8400e29b41d4a716446655440000", false},
		{"wrong length", "550e8400-e29b-41d4", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsUuid(tt.value)
			if result != tt.expected {
				t.Errorf("IsUuid(%q) = %v; want %v", tt.value, result, tt.expected)
			}
		})
	}
}

func TestWordWrap(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		width    int
		breakStr string
		expected string
	}{
		{"basic wrap", "The quick brown fox jumps over the lazy dog", 20, "\n", "The quick brown fox\njumps over the lazy\ndog"},
		{"custom break", "The quick brown fox", 10, "<br>", "The quick<br>brown fox"},
		{"no wrap needed", "Short text", 20, "\n", "Short text"},
		{"single long word", "supercalifragilisticexpialidocious", 10, "\n", "supercalifragilisticexpialidocious"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WordWrap(tt.input, tt.width, tt.breakStr)
			if result != tt.expected {
				t.Errorf("WordWrap(%q, %d, %q) = %q; want %q", tt.input, tt.width, tt.breakStr, result, tt.expected)
			}
		})
	}
}

func TestStringable(t *testing.T) {
	t.Run("chaining operations", func(t *testing.T) {
		result := Of("hello_world_example").
			Camel().
			UcFirst().
			Limit(15, "...").
			String()

		expected := "HelloWorldExamp..."
		if result != expected {
			t.Errorf("Stringable chain = %q; want %q", result, expected)
		}
	})

	t.Run("after before chain", func(t *testing.T) {
		result := Of("App\\Http\\Controllers\\Controller").
			After("App\\").
			Before("\\Controller").
			String()

		expected := "Http"
		if result != expected {
			t.Errorf("Stringable after/before = %q; want %q", result, expected)
		}
	})

	t.Run("case conversions", func(t *testing.T) {
		input := "hello_world"

		if got := Of(input).Camel().String(); got != "helloWorld" {
			t.Errorf("Camel = %q; want %q", got, "helloWorld")
		}

		if got := Of(input).Studly().String(); got != "HelloWorld" {
			t.Errorf("Studly = %q; want %q", got, "HelloWorld")
		}

		if got := Of(input).Kebab().String(); got != "hello-world" {
			t.Errorf("Kebab = %q; want %q", got, "hello-world")
		}
	})

	t.Run("slug generation", func(t *testing.T) {
		result := Of("Velocity 1 Framework!").
			Slug("-").
			String()

		expected := "velocity-1-framework"
		if result != expected {
			t.Errorf("Slug = %q; want %q", result, expected)
		}
	})
}
func TestBeforeLast(t *testing.T) {
	tests := []struct {
		name     string
		subject  string
		search   string
		expected string
	}{
		{"basic", "This is my name", " ", "This is my"},
		{"not found", "Hannah", "Hello", "Hannah"},
		{"at beginning", "test", "t", "tes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BeforeLast(tt.subject, tt.search)
			if result != tt.expected {
				t.Errorf("BeforeLast(%q, %q) = %q; want %q", tt.subject, tt.search, result, tt.expected)
			}
		})
	}
}

func TestBetweenFirst(t *testing.T) {
	result := BetweenFirst("[a] bc [d]", "[", "]")
	if result != "a" {
		t.Errorf("BetweenFirst = %q; want %q", result, "a")
	}
}

func TestContainsAll(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		needles  []string
		expected bool
	}{
		{"all present", "This is my name", []string{"my", "name"}, true},
		{"some missing", "This is my name", []string{"my", "foo"}, false},
		{"empty", "test", []string{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ContainsAll(tt.haystack, tt.needles)
			if result != tt.expected {
				t.Errorf("ContainsAll = %v; want %v", result, tt.expected)
			}
		})
	}
}

func TestExactly(t *testing.T) {
	if !Exactly("Velocity", "Velocity") {
		t.Error("Exactly should return true for identical strings")
	}
	if Exactly("Velocity", "velocity") {
		t.Error("Exactly should return false for different case")
	}
}

func TestExcerpt(t *testing.T) {
	result := Excerpt("This is my name", "my", ExcerptOptions{Radius: 3})
	if !Contains(result, "my") {
		t.Errorf("Excerpt should contain 'my': %q", result)
	}
}

func TestFinish(t *testing.T) {
	tests := []struct {
		value    string
		cap      string
		expected string
	}{
		{"test", "/", "test/"},
		{"test/", "/", "test/"},
	}
	for _, tt := range tests {
		result := Finish(tt.value, tt.cap)
		if result != tt.expected {
			t.Errorf("Finish(%q, %q) = %q; want %q", tt.value, tt.cap, result, tt.expected)
		}
	}
}

func TestHeadline(t *testing.T) {
	result := Headline("taylor_otwell")
	expected := "Taylor Otwell"
	if result != expected {
		t.Errorf("Headline = %q; want %q", result, expected)
	}
}

func TestIs(t *testing.T) {
	tests := []struct {
		pattern  string
		value    string
		expected bool
	}{
		{"foo*", "foobar", true},
		{"foo*", "barfoo", false},
		{"*bar", "foobar", true},
	}
	for _, tt := range tests {
		result := Is(tt.pattern, tt.value)
		if result != tt.expected {
			t.Errorf("Is(%q, %q) = %v; want %v", tt.pattern, tt.value, result, tt.expected)
		}
	}
}

func TestIsAscii(t *testing.T) {
	if !IsAscii("Taylor") {
		t.Error("IsAscii should return true for ASCII string")
	}
	if IsAscii("ü") {
		t.Error("IsAscii should return false for non-ASCII")
	}
}

func TestIsJson(t *testing.T) {
	if !IsJson(`{"name":"John"}`) {
		t.Error("IsJson should return true for valid JSON")
	}
	if IsJson("not json") {
		t.Error("IsJson should return false for invalid JSON")
	}
}

func TestIsUlid(t *testing.T) {
	if !IsUlid("01ARZ3NDEKTSV4RRFFQ69G5FAV") {
		t.Error("IsUlid should return true for valid ULID")
	}
	if IsUlid("invalid") {
		t.Error("IsUlid should return false for invalid ULID")
	}
}

func TestIsUrl(t *testing.T) {
	if !IsUrl("https://velocity.dev") {
		t.Error("IsUrl should return true for valid URL")
	}
	if IsUrl("not a url") {
		t.Error("IsUrl should return false for invalid URL")
	}
}

func TestLength(t *testing.T) {
	if Length("Velocity") != 8 {
		t.Error("Length should return 8")
	}
}

func TestLower(t *testing.T) {
	if Lower("VELOCITY") != "velocity" {
		t.Error("Lower should convert to lowercase")
	}
}

func TestWords(t *testing.T) {
	result := Words("This is my name", 2)
	if result != "This is..." {
		t.Errorf("Words = %q; want %q", result, "This is...")
	}
}

func TestMatch(t *testing.T) {
	if !Match("^foo", "foobar") {
		t.Error("Match should return true")
	}
	if Match("^bar", "foobar") {
		t.Error("Match should return false")
	}
}

func TestMatchAll(t *testing.T) {
	result := MatchAll("bar", "bar foo bar")
	if len(result) != 2 {
		t.Errorf("MatchAll should find 2 matches, got %d", len(result))
	}
}

func TestTest(t *testing.T) {
	if !Test("^Velocity", "Velocity Framework") {
		t.Error("Test should return true")
	}
}

func TestPadBoth(t *testing.T) {
	result := PadBoth("James", 10)
	if len(result) != 10 {
		t.Errorf("PadBoth length = %d; want 10", len(result))
	}
}

func TestPadLeft(t *testing.T) {
	result := PadLeft("James", 10, "-")
	expected := "-----James"
	if result != expected {
		t.Errorf("PadLeft = %q; want %q", result, expected)
	}
}

func TestPadRight(t *testing.T) {
	result := PadRight("James", 10, "-")
	expected := "James-----"
	if result != expected {
		t.Errorf("PadRight = %q; want %q", result, expected)
	}
}

func TestPlural(t *testing.T) {
	if Plural("car", 1) != "car" {
		t.Error("Plural with count 1 should return singular")
	}
	if Plural("car", 2) != "cars" {
		t.Error("Plural with count 2 should return plural")
	}
}

func TestPosition(t *testing.T) {
	if Position("hello world", "world") != 6 {
		t.Error("Position should return 6")
	}
	if Position("hello", "bye") != -1 {
		t.Error("Position should return -1 for not found")
	}
}

func TestAscii(t *testing.T) {
	result := Ascii("ü")
	if result == "ü" {
		t.Error("Ascii should transliterate non-ASCII characters")
	}
}

func TestMarkdown(t *testing.T) {
	result := Markdown("# Velocity")
	if !Contains(result, "Velocity") {
		t.Error("Markdown should contain 'Velocity'")
	}
}

func TestSquish(t *testing.T) {
	result := Squish("   velocity   framework   ")
	expected := "velocity framework"
	if result != expected {
		t.Errorf("Squish = %q; want %q", result, expected)
	}
}

func TestSquishFast(t *testing.T) {
	result := SquishFast("   velocity   framework   ")
	expected := "velocity framework"
	if result != expected {
		t.Errorf("SquishFast = %q; want %q", result, expected)
	}
}

func TestRandom(t *testing.T) {
	result, err := Random(10)
	if err != nil {
		t.Fatalf("Random(10) returned error: %v", err)
	}
	if len(result) != 10 {
		t.Errorf("Random length = %d; want 10", len(result))
	}
}

func TestRandom_Default(t *testing.T) {
	got, err := Random()
	if err != nil {
		t.Fatalf("Random() returned error: %v", err)
	}
	if len(got) != 16 {
		t.Errorf("Random() length = %d; want 16", len(got))
	}
}

func TestRandom_Zero(t *testing.T) {
	got, err := Random(0)
	if err != nil {
		t.Fatalf("Random(0) returned error: %v", err)
	}
	if got != "" {
		t.Errorf("Random(0) = %q; want empty string", got)
	}
}

func TestRandom_NotDeterministic(t *testing.T) {
	a, err := Random(32)
	if err != nil {
		t.Fatalf("Random(32) returned error: %v", err)
	}
	b, err := Random(32)
	if err != nil {
		t.Fatalf("Random(32) returned error: %v", err)
	}
	if a == b {
		t.Fatalf("Random(32) returned identical strings on consecutive calls: %q", a)
	}
}

func TestRandom_AlphabetMembership(t *testing.T) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	out, err := Random(64)
	if err != nil {
		t.Fatalf("Random(64) returned error: %v", err)
	}
	if len(out) != 64 {
		t.Fatalf("Random(64) length = %d; want 64", len(out))
	}
	for i, r := range out {
		if !strings.ContainsRune(alphabet, r) {
			t.Fatalf("Random(64) byte %d = %q not in alphabet", i, r)
		}
	}
}

// TestRandom_EntropyFailure swaps the entropy source for a reader that always
// fails. Random must return an error rather than panicking.
func TestRandom_EntropyFailure(t *testing.T) {
	orig := randReader
	t.Cleanup(func() { randReader = orig })

	randReader = errReader{}
	got, err := Random(16)
	if err == nil {
		t.Fatalf("Random with failing entropy: want error, got %q", got)
	}
	if got != "" {
		t.Errorf("Random with failing entropy: want empty string, got %q", got)
	}
}

// TestRandom_Distribution draws a large sample and checks every letter in the
// 62-character alphabet appears within a tight tolerance of the uniform
// expectation. Modulo bias would skew 8 letters by 1.25x relative to the
// rest; the tolerance below would fail in that case.
func TestRandom_Distribution(t *testing.T) {
	if testing.Short() {
		t.Skip("distribution test takes ~1M samples")
	}
	const total = 1_000_000
	out, err := Random(total)
	if err != nil {
		t.Fatalf("Random(%d) error: %v", total, err)
	}
	counts := make(map[rune]int, 62)
	for _, r := range out {
		counts[r]++
	}
	if len(counts) != 62 {
		t.Fatalf("alphabet coverage: got %d distinct letters, want 62", len(counts))
	}
	expected := float64(total) / 62.0
	// Tolerance of 10% catches the 1.25x modulo-bias signature easily and
	// stays well above the natural binomial noise at this sample size.
	const tolerance = 0.10
	for r, c := range counts {
		ratio := float64(c) / expected
		if ratio < 1-tolerance || ratio > 1+tolerance {
			t.Errorf("letter %q count = %d (ratio %.3f vs expected %.0f); outside +/-%.0f%%",
				r, c, ratio, expected, tolerance*100)
		}
	}
}

// errReader implements io.Reader and always returns an error. Used to
// exercise the entropy-failure path in Random.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) {
	return 0, errReaderErr
}

var errReaderErr = errReaderError("simulated entropy failure")

type errReaderError string

func (e errReaderError) Error() string { return string(e) }

func TestRepeat(t *testing.T) {
	if Repeat("a", 3) != "aaa" {
		t.Error("Repeat failed")
	}
}

func TestReplace(t *testing.T) {
	result := Replace("foo", "bar", "foo test")
	if result != "bar test" {
		t.Errorf("Replace = %q; want %q", result, "bar test")
	}
}

func TestReplaceArray(t *testing.T) {
	result := ReplaceArray([]string{"foo", "bar"}, []string{"1", "2"}, "foo and bar")
	if result != "1 and 2" {
		t.Errorf("ReplaceArray = %q", result)
	}
}

func TestReplaceFirst(t *testing.T) {
	result := ReplaceFirst("foo", "bar", "foo foo")
	if result != "bar foo" {
		t.Errorf("ReplaceFirst = %q", result)
	}
}

func TestReplaceLast(t *testing.T) {
	result := ReplaceLast("foo", "bar", "foo foo")
	if result != "foo bar" {
		t.Errorf("ReplaceLast = %q", result)
	}
}

func TestSingular(t *testing.T) {
	if Singular("cars") != "car" {
		t.Error("Singular failed")
	}
}

func TestSlugWithSeparator(t *testing.T) {
	result := Slug("Hello World", "-")
	if result != "hello-world" {
		t.Errorf("Slug = %q", result)
	}
}

func TestSubstr(t *testing.T) {
	result := Substr("Hello World", 0, 5)
	if result != "Hello" {
		t.Errorf("Substr = %q", result)
	}
}

func TestSubstrCount(t *testing.T) {
	count := SubstrCount("foo bar foo", "foo")
	if count != 2 {
		t.Errorf("SubstrCount = %d; want 2", count)
	}
}

func TestSwap(t *testing.T) {
	result := Swap(map[string]string{"foo": "bar", "baz": "qux"}, "foo is baz")
	if result != "bar is qux" {
		t.Errorf("Swap = %q", result)
	}
}

func TestTitle(t *testing.T) {
	if Title("hello world") != "Hello World" {
		t.Error("Title failed")
	}
}

func TestUpper(t *testing.T) {
	if Upper("hello") != "HELLO" {
		t.Error("Upper failed")
	}
}

func TestWhen(t *testing.T) {
	result := Of("test").When(true, func(s *Stringable) *Stringable {
		return s.Upper()
	}).String()
	if result != "TEST" {
		t.Errorf("When = %q", result)
	}
}

func TestWhenContains(t *testing.T) {
	result := Of("foo bar").WhenContains("foo", func(s *Stringable) *Stringable {
		return s.Upper()
	}).String()
	if result != "FOO BAR" {
		t.Errorf("WhenContains = %q", result)
	}
}

func TestWhenEmpty(t *testing.T) {
	result := Of("").WhenEmpty(func(s *Stringable) *Stringable {
		s.value = "default"
		return s
	}).String()
	if result != "default" {
		t.Errorf("WhenEmpty = %q", result)
	}
}

func TestTrim(t *testing.T) {
	if Trim("  hello  ") != "hello" {
		t.Error("Trim failed")
	}
}
func TestStart(t *testing.T) {
	result := Start("world", "hello ")
	if result != "hello world" {
		t.Errorf("Start = %q", result)
	}
	result2 := Start("hello world", "hello ")
	if result2 != "hello world" {
		t.Errorf("Start should not duplicate prefix")
	}
}

func TestSubstrReplace(t *testing.T) {
	result := SubstrReplace("Hello World", "Goodbye", 0, 5)
	if result != "Goodbye World" {
		t.Errorf("SubstrReplace = %q", result)
	}
}

func TestTake(t *testing.T) {
	result := Take("Hello World", 5)
	if result != "Hello" {
		t.Errorf("Take = %q", result)
	}
	result2 := Take("Hello", -3)
	if result2 != "llo" {
		t.Errorf("Take negative = %q", result2)
	}
}

func TestLtrim(t *testing.T) {
	result := Ltrim("  hello  ")
	if result != "hello  " {
		t.Errorf("Ltrim = %q", result)
	}
}

func TestRtrim(t *testing.T) {
	result := Rtrim("  hello  ")
	if result != "  hello" {
		t.Errorf("Rtrim = %q", result)
	}
}

func TestUcfirst(t *testing.T) {
	result := Ucfirst("hello world")
	if result != "Hello world" {
		t.Errorf("Ucfirst = %q", result)
	}
}

func TestUcsplit(t *testing.T) {
	result := Ucsplit("HelloWorld")
	if len(result) != 2 || result[0] != "Hello" || result[1] != "World" {
		t.Errorf("Ucsplit failed: %v", result)
	}
}

func TestWhenContainsAll(t *testing.T) {
	result := WhenContainsAll("foo bar baz", []string{"foo", "bar"}, func(s string) string {
		return Upper(s)
	})
	if result != "FOO BAR BAZ" {
		t.Errorf("WhenContainsAll = %q", result)
	}
	result2 := WhenContainsAll("foo bar", []string{"foo", "baz"}, func(s string) string {
		return Upper(s)
	})
	if result2 != "foo bar" {
		t.Errorf("WhenContainsAll should not apply when not all needles present")
	}
}

func TestWhenNotEmpty(t *testing.T) {
	result := WhenNotEmpty("hello", func(s string) string {
		return Upper(s)
	})
	if result != "HELLO" {
		t.Errorf("WhenNotEmpty = %q", result)
	}
	result2 := WhenNotEmpty("", func(s string) string {
		return Upper(s)
	})
	if result2 != "" {
		t.Errorf("WhenNotEmpty should not apply on empty string")
	}
}

func TestWhenStartsWith(t *testing.T) {
	result := WhenStartsWith("hello world", []string{"hello"}, func(s string) string {
		return Upper(s)
	})
	if result != "HELLO WORLD" {
		t.Errorf("WhenStartsWith = %q", result)
	}
}

func TestWhenEndsWith(t *testing.T) {
	result := WhenEndsWith("hello world", []string{"world"}, func(s string) string {
		return Upper(s)
	})
	if result != "HELLO WORLD" {
		t.Errorf("WhenEndsWith = %q", result)
	}
}

func TestWhenExactly(t *testing.T) {
	result := WhenExactly("hello", "hello", func(s string) string {
		return Upper(s)
	})
	if result != "HELLO" {
		t.Errorf("WhenExactly = %q", result)
	}
}

func TestWhenNotExactly(t *testing.T) {
	result := WhenNotExactly("hello", "world", func(s string) string {
		return Upper(s)
	})
	if result != "HELLO" {
		t.Errorf("WhenNotExactly = %q", result)
	}
}

func TestWhenIs(t *testing.T) {
	result := WhenIs("foo*", "foobar", func(s string) string {
		return Upper(s)
	})
	if result != "FOOBAR" {
		t.Errorf("WhenIs = %q", result)
	}
}

func TestWhenIsAscii(t *testing.T) {
	result := WhenIsAscii("hello", func(s string) string {
		return Upper(s)
	})
	if result != "HELLO" {
		t.Errorf("WhenIsAscii = %q", result)
	}
}

func TestWhenIsUlid(t *testing.T) {
	ulid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	result := WhenIsUlid(ulid, func(s string) string {
		return Lower(s)
	})
	if result != Lower(ulid) {
		t.Errorf("WhenIsUlid failed")
	}
}

func TestWhenIsUuid(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	result := WhenIsUuid(uuid, func(s string) string {
		return Upper(s)
	})
	if result != Upper(uuid) {
		t.Errorf("WhenIsUuid failed")
	}
}

func TestWhenTest(t *testing.T) {
	result := WhenTest("^[a-z]+$", "hello", func(s string) string {
		return Upper(s)
	})
	if result != "HELLO" {
		t.Errorf("WhenTest = %q", result)
	}
}

func TestWordCount(t *testing.T) {
	count := WordCount("hello world foo bar")
	if count != 4 {
		t.Errorf("WordCount = %d; want 4", count)
	}
}

func TestInlineMarkdown(t *testing.T) {
	result := InlineMarkdown("**bold** text")
	if result != "bold text" {
		t.Errorf("InlineMarkdown = %q; want %q", result, "bold text")
	}
}

func TestReplaceMatches(t *testing.T) {
	result := Of("foo123bar456").ReplaceMatches("[0-9]+", "X").String()
	if result != "fooXbarX" {
		t.Errorf("ReplaceMatches = %q", result)
	}
}

func TestAppend(t *testing.T) {
	result := Of("Hello").Append(" World", "!").String()
	if result != "Hello World!" {
		t.Errorf("Append = %q", result)
	}
}

func TestPrepend(t *testing.T) {
	result := Of("World").Prepend("Hello ", "Beautiful ").String()
	if result != "Hello Beautiful World" {
		t.Errorf("Prepend = %q", result)
	}
}

func TestBasename(t *testing.T) {
	result := Of("/path/to/file.txt").Basename().String()
	if result != "file.txt" {
		t.Errorf("Basename = %q", result)
	}
}

func TestDirname(t *testing.T) {
	result := Of("/path/to/file.txt").Dirname().String()
	if result != "/path/to" {
		t.Errorf("Dirname = %q", result)
	}
}

func TestExactlyStringable(t *testing.T) {
	s1 := Of("test")
	if !s1.Exactly("test") {
		t.Error("Exactly should return true for same string")
	}
}

func TestWhenStringable(t *testing.T) {
	result := Of("test").When(true, func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "TEST" {
		t.Error("When with Stringable failed")
	}
}

func TestWhenContainsStringable(t *testing.T) {
	result := Of("foo bar").WhenContains("foo", func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "FOO BAR" {
		t.Error("WhenContains with Stringable failed")
	}
}

func TestWhenContainsAllStringable(t *testing.T) {
	result := Of("foo bar baz").WhenContainsAll([]string{"foo", "bar"}, func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "FOO BAR BAZ" {
		t.Error("WhenContainsAll with Stringable failed")
	}
}

func TestWhenEmptyStringable(t *testing.T) {
	result := Of("").WhenEmpty(func(s *Stringable) *Stringable {
		s.value = "default"
		return s
	})
	if result.String() != "default" {
		t.Error("WhenEmpty with Stringable failed")
	}
}

func TestWhenNotEmptyStringable(t *testing.T) {
	result := Of("test").WhenNotEmpty(func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "TEST" {
		t.Error("WhenNotEmpty with Stringable failed")
	}
}

func TestWhenStartsWithStringable(t *testing.T) {
	result := Of("hello world").WhenStartsWith([]string{"hello"}, func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "HELLO WORLD" {
		t.Error("WhenStartsWith with Stringable failed")
	}
}

func TestWhenEndsWithStringable(t *testing.T) {
	result := Of("hello world").WhenEndsWith([]string{"world"}, func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "HELLO WORLD" {
		t.Error("WhenEndsWith with Stringable failed")
	}
}

func TestWhenExactlyStringable(t *testing.T) {
	result := Of("test").WhenExactly("test", func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "TEST" {
		t.Error("WhenExactly with Stringable failed")
	}
}

func TestWhenNotExactlyStringable(t *testing.T) {
	result := Of("test").WhenNotExactly("other", func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "TEST" {
		t.Error("WhenNotExactly with Stringable failed")
	}
}

func TestWhenIsStringable(t *testing.T) {
	result := Of("foobar").WhenIs("foo*", func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "FOOBAR" {
		t.Error("WhenIs with Stringable failed")
	}
}

func TestWhenIsAsciiStringable(t *testing.T) {
	result := Of("hello").WhenIsAscii(func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "HELLO" {
		t.Error("WhenIsAscii with Stringable failed")
	}
}

func TestWhenIsUlidStringable(t *testing.T) {
	ulid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	result := Of(ulid).WhenIsUlid(func(s *Stringable) *Stringable {
		return s.Lower()
	})
	if result.String() != Lower(ulid) {
		t.Error("WhenIsUlid with Stringable failed")
	}
}

func TestWhenIsUuidStringable(t *testing.T) {
	uuid := "550e8400-e29b-41d4-a716-446655440000"
	result := Of(uuid).WhenIsUuid(func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != Upper(uuid) {
		t.Error("WhenIsUuid with Stringable failed")
	}
}

func TestWhenTestStringable(t *testing.T) {
	result := Of("hello").WhenTest("^[a-z]+$", func(s *Stringable) *Stringable {
		return s.Upper()
	})
	if result.String() != "HELLO" {
		t.Error("WhenTest with Stringable failed")
	}
}

func TestNewLine(t *testing.T) {
	result := Of("Hello").NewLine().Append("World").String()
	if result != "Hello\nWorld" {
		t.Errorf("NewLine = %q", result)
	}
}

func TestSplit(t *testing.T) {
	parts := Of("foo,bar,baz").Split(",")
	if len(parts) != 3 || parts[0] != "foo" || parts[1] != "bar" || parts[2] != "baz" {
		t.Errorf("Split failed: %v", parts)
	}
}

func TestTap(t *testing.T) {
	tapped := ""
	result := Of("test").Tap(func(s *Stringable) {
		tapped = s.String()
	})
	if tapped != "test" || result.String() != "test" {
		t.Error("Tap failed")
	}
}

func TestPipe(t *testing.T) {
	result := Of("test").Pipe(func(s string) interface{} {
		return s + "!"
	})
	if result != "test!" {
		t.Error("Pipe failed")
	}
}

func TestToStringable(t *testing.T) {
	result := Of("test").ToString()
	if result != "test" {
		t.Error("ToString failed")
	}
}

// Additional Stringable method tests for better coverage
func TestStringableCharAt(t *testing.T) {
	result := Of("hello").CharAt(1)
	if result != "e" {
		t.Errorf("CharAt = %q; want %q", result, "e")
	}
}

func TestStringableClassBasename(t *testing.T) {
	result := Of("App.Http.Controllers.Controller").ClassBasename().String()
	if result != "Controller" {
		t.Errorf("ClassBasename = %q", result)
	}
}

func TestStringableContains(t *testing.T) {
	if !Of("hello world").Contains("world") {
		t.Error("Contains should return true")
	}
}

func TestStringableContainsAll(t *testing.T) {
	if !Of("hello world").ContainsAll([]string{"hello", "world"}) {
		t.Error("ContainsAll should return true")
	}
}

func TestStringableEndsWith(t *testing.T) {
	if !Of("hello world").EndsWith("world") {
		t.Error("EndsWith should return true")
	}
}

func TestStringableExcerpt(t *testing.T) {
	result := Of("This is my name").Excerpt("my", ExcerptOptions{Radius: 3}).String()
	if !Contains(result, "my") {
		t.Error("Excerpt should contain 'my'")
	}
}

func TestStringableExplode(t *testing.T) {
	parts := Of("foo,bar,baz").Explode(",")
	if len(parts) != 3 {
		t.Errorf("Explode length = %d; want 3", len(parts))
	}
}

func TestStringableFinish(t *testing.T) {
	result := Of("test").Finish("/").String()
	if result != "test/" {
		t.Errorf("Finish = %q", result)
	}
}

func TestStringableHeadline(t *testing.T) {
	result := Of("taylor_otwell").Headline().String()
	if result != "Taylor Otwell" {
		t.Errorf("Headline = %q", result)
	}
}

func TestStringableInlineMarkdown(t *testing.T) {
	result := Of("**bold** text").InlineMarkdown().String()
	if result != "bold text" {
		t.Errorf("InlineMarkdown = %q", result)
	}
}

func TestStringableIs(t *testing.T) {
	if !Of("foobar").Is("foo*") {
		t.Error("Is should return true")
	}
}

func TestStringableIsAscii(t *testing.T) {
	if !Of("hello").IsAscii() {
		t.Error("IsAscii should return true")
	}
}

func TestStringableIsEmpty(t *testing.T) {
	if !Of("").IsEmpty() {
		t.Error("IsEmpty should return true")
	}
}

func TestStringableIsNotEmpty(t *testing.T) {
	if !Of("test").IsNotEmpty() {
		t.Error("IsNotEmpty should return true")
	}
}

func TestStringableIsJson(t *testing.T) {
	if !Of(`{"key":"value"}`).IsJson() {
		t.Error("IsJson should return true")
	}
}

func TestStringableIsUlid(t *testing.T) {
	if !Of("01ARZ3NDEKTSV4RRFFQ69G5FAV").IsUlid() {
		t.Error("IsUlid should return true")
	}
}

func TestStringableIsUrl(t *testing.T) {
	if !Of("https://example.com").IsUrl() {
		t.Error("IsUrl should return true")
	}
}

func TestStringableIsUuid(t *testing.T) {
	if !Of("550e8400-e29b-41d4-a716-446655440000").IsUuid() {
		t.Error("IsUuid should return true")
	}
}

func TestStringableLcFirst(t *testing.T) {
	result := Of("Hello").LcFirst().String()
	if result != "hello" {
		t.Errorf("LcFirst = %q", result)
	}
}

func TestStringableLength(t *testing.T) {
	if Of("hello").Length() != 5 {
		t.Error("Length should return 5")
	}
}

func TestStringableLtrim(t *testing.T) {
	result := Of("  hello").Ltrim().String()
	if result != "hello" {
		t.Errorf("Ltrim = %q", result)
	}
}

func TestStringableMarkdown(t *testing.T) {
	result := Of("# Hello").Markdown().String()
	if !Contains(result, "Hello") {
		t.Error("Markdown should contain 'Hello'")
	}
}

func TestStringableMask(t *testing.T) {
	result := Of("secret").Mask('*', 0, 3).String()
	if result != "***ret" {
		t.Errorf("Mask = %q", result)
	}
}

func TestStringableMatch(t *testing.T) {
	if !Of("foobar").Match("^foo") {
		t.Error("Match should return true")
	}
}

func TestStringableMatchAll(t *testing.T) {
	matches := Of("bar foo bar").MatchAll("bar")
	if len(matches) != 2 {
		t.Errorf("MatchAll length = %d; want 2", len(matches))
	}
}

func TestStringablePadBoth(t *testing.T) {
	result := Of("test").PadBoth(10).String()
	if len(result) != 10 {
		t.Errorf("PadBoth length = %d; want 10", len(result))
	}
}

func TestStringablePadLeft(t *testing.T) {
	result := Of("test").PadLeft(10, "-").String()
	if !StartsWith(result, "---") {
		t.Error("PadLeft should start with dashes")
	}
}

func TestStringablePadRight(t *testing.T) {
	result := Of("test").PadRight(10, "-").String()
	if !EndsWith(result, "---") {
		t.Error("PadRight should end with dashes")
	}
}

func TestStringablePlural(t *testing.T) {
	result := Of("car").Plural(2).String()
	if result != "cars" {
		t.Errorf("Plural = %q", result)
	}
}

func TestStringablePosition(t *testing.T) {
	pos := Of("hello world").Position("world")
	if pos != 6 {
		t.Errorf("Position = %d; want 6", pos)
	}
}

func TestStringableRemove(t *testing.T) {
	result := Of("hello world").Remove("world").String()
	if result != "hello " {
		t.Errorf("Remove = %q", result)
	}
}

func TestStringableRepeat(t *testing.T) {
	result := Of("a").Repeat(3).String()
	if result != "aaa" {
		t.Errorf("Repeat = %q", result)
	}
}

func TestStringableReplace(t *testing.T) {
	result := Of("foo bar").Replace("foo", "baz").String()
	if result != "baz bar" {
		t.Errorf("Replace = %q", result)
	}
}

func TestStringableReplaceArray(t *testing.T) {
	result := Of("foo bar").ReplaceArray([]string{"foo", "bar"}, []string{"1", "2"}).String()
	if result != "1 2" {
		t.Errorf("ReplaceArray = %q", result)
	}
}

func TestStringableReplaceFirst(t *testing.T) {
	result := Of("foo foo").ReplaceFirst("foo", "bar").String()
	if result != "bar foo" {
		t.Errorf("ReplaceFirst = %q", result)
	}
}

func TestStringableReplaceLast(t *testing.T) {
	result := Of("foo foo").ReplaceLast("foo", "bar").String()
	if result != "foo bar" {
		t.Errorf("ReplaceLast = %q", result)
	}
}

func TestStringableReplaceMatches(t *testing.T) {
	result := Of("foo123").ReplaceMatches("[0-9]+", "X").String()
	if result != "fooX" {
		t.Errorf("ReplaceMatches = %q", result)
	}
}

func TestStringableReverse(t *testing.T) {
	result := Of("hello").Reverse().String()
	if result != "olleh" {
		t.Errorf("Reverse = %q", result)
	}
}

func TestStringableRtrim(t *testing.T) {
	result := Of("hello  ").Rtrim().String()
	if result != "hello" {
		t.Errorf("Rtrim = %q", result)
	}
}

func TestStringableSingular(t *testing.T) {
	result := Of("cars").Singular().String()
	if result != "car" {
		t.Errorf("Singular = %q", result)
	}
}

func TestStringableSlug(t *testing.T) {
	result := Of("Hello World").Slug("-").String()
	if result != "hello-world" {
		t.Errorf("Slug = %q", result)
	}
}

func TestStringableSnake(t *testing.T) {
	result := Of("HelloWorld").Snake().String()
	if result != "hello_world" {
		t.Errorf("Snake = %q", result)
	}
}

func TestStringableSplit(t *testing.T) {
	parts := Of("a,b,c").Split(",")
	if len(parts) != 3 {
		t.Errorf("Split length = %d; want 3", len(parts))
	}
}

func TestStringableSquish(t *testing.T) {
	result := Of("  hello  world  ").Squish().String()
	if result != "hello world" {
		t.Errorf("Squish = %q", result)
	}
}

func TestStringableStart(t *testing.T) {
	result := Of("world").Start("hello ").String()
	if result != "hello world" {
		t.Errorf("Start = %q", result)
	}
}

func TestStringableStartsWith(t *testing.T) {
	if !Of("hello world").StartsWith("hello") {
		t.Error("StartsWith should return true")
	}
}

func TestStringableStudly(t *testing.T) {
	result := Of("hello_world").Studly().String()
	if result != "HelloWorld" {
		t.Errorf("Studly = %q", result)
	}
}

func TestStringableSubstr(t *testing.T) {
	result := Of("hello world").Substr(0, 5).String()
	if result != "hello" {
		t.Errorf("Substr = %q", result)
	}
}

func TestStringableSubstrCount(t *testing.T) {
	count := Of("foo bar foo").SubstrCount("foo")
	if count != 2 {
		t.Errorf("SubstrCount = %d; want 2", count)
	}
}

func TestStringableSubstrReplace(t *testing.T) {
	result := Of("hello world").SubstrReplace("goodbye", 0, 5).String()
	if result != "goodbye world" {
		t.Errorf("SubstrReplace = %q", result)
	}
}

func TestStringableSwap(t *testing.T) {
	result := Of("foo bar").Swap(map[string]string{"foo": "1", "bar": "2"}).String()
	if result != "1 2" {
		t.Errorf("Swap = %q", result)
	}
}

func TestStringableTake(t *testing.T) {
	result := Of("hello").Take(3).String()
	if result != "hel" {
		t.Errorf("Take = %q", result)
	}
}

func TestStringableTest(t *testing.T) {
	if !Of("hello").Test("^[a-z]+$") {
		t.Error("Test should return true")
	}
}

func TestStringableTitle(t *testing.T) {
	result := Of("hello world").Title().String()
	if result != "Hello World" {
		t.Errorf("Title = %q", result)
	}
}

func TestStringableTrim(t *testing.T) {
	result := Of("  hello  ").Trim().String()
	if result != "hello" {
		t.Errorf("Trim = %q", result)
	}
}

func TestStringableUcFirst(t *testing.T) {
	result := Of("hello").UcFirst().String()
	if result != "Hello" {
		t.Errorf("UcFirst = %q", result)
	}
}

func TestStringableUcSplit(t *testing.T) {
	parts := Of("FooBar").UcSplit()
	if len(parts) != 2 {
		t.Errorf("UcSplit length = %d; want 2", len(parts))
	}
}

func TestStringableUnless(t *testing.T) {
	result := Of("test").Unless(false, func(s *Stringable) *Stringable {
		return s.Upper()
	}).String()
	if result != "TEST" {
		t.Errorf("Unless = %q", result)
	}
}

func TestStringableWords(t *testing.T) {
	result := Of("hello world foo bar").Words(2).String()
	if result != "hello world..." {
		t.Errorf("Words = %q", result)
	}
}

func TestStringableWordWrap(t *testing.T) {
	result := Of("hello world").WordWrap(5, "\n").String()
	if !Contains(result, "\n") {
		t.Error("WordWrap should contain newline")
	}
}

func TestStringableWordCount(t *testing.T) {
	count := Of("hello world foo").WordCount()
	if count != 3 {
		t.Errorf("WordCount = %d; want 3", count)
	}
}

// Additional tests for uncovered edge cases
func TestStringableAfterLast(t *testing.T) {
	result := Of("App\\Http\\Controllers\\Controller").AfterLast("\\").String()
	if result != "Controller" {
		t.Errorf("AfterLast = %q", result)
	}
}

func TestStringableAscii(t *testing.T) {
	result := Of("ü").Ascii().String()
	if result == "ü" {
		t.Error("Ascii should transliterate")
	}
}

func TestStringableBeforeLast(t *testing.T) {
	result := Of("This is my name").BeforeLast(" ").String()
	if result != "This is my" {
		t.Errorf("BeforeLast = %q", result)
	}
}

func TestStringableBetween(t *testing.T) {
	result := Of("[a] bc [d]").Between("[", "]").String()
	if result != "a" {
		t.Errorf("Between = %q", result)
	}
}

func TestStringableBetweenFirst(t *testing.T) {
	result := Of("[a] bc [d]").BetweenFirst("[", "]").String()
	if result != "a" {
		t.Errorf("BetweenFirst = %q", result)
	}
}

func TestStringableUnlessWithDefault(t *testing.T) {
	result := Of("test").Unless(true, func(s *Stringable) *Stringable {
		return s.Upper()
	}, func(s *Stringable) *Stringable {
		return s.Lower()
	}).String()
	if result != "test" {
		t.Errorf("Unless with default = %q", result)
	}
}

func TestStringableWhenWithDefault(t *testing.T) {
	result := Of("TEST").When(false, func(s *Stringable) *Stringable {
		return s.Lower()
	}, func(s *Stringable) *Stringable {
		return s.Upper()
	}).String()
	if result != "TEST" {
		t.Errorf("When with default = %q", result)
	}
}

func TestStringableCharAtOutOfBounds(t *testing.T) {
	result := Of("hello").CharAt(10)
	if result != "" {
		t.Error("CharAt out of bounds should return empty string")
	}
}

func TestStringableDirnameMultipleLevels(t *testing.T) {
	result := Of("/path/to/file.txt").Dirname(2).String()
	if result != "/path" {
		t.Errorf("Dirname(2) = %q", result)
	}
}

func TestStringableExplodeWithLimit(t *testing.T) {
	parts := Of("a,b,c,d").Explode(",", 2)
	if len(parts) != 2 {
		t.Errorf("Explode with limit length = %d; want 2", len(parts))
	}
}

func TestStringableNewLineMultiple(t *testing.T) {
	result := Of("test").NewLine(2).String()
	if result != "test\n\n" {
		t.Errorf("NewLine(2) = %q", result)
	}
}

func TestStringableLcFirstEmpty(t *testing.T) {
	result := Of("").LcFirst().String()
	if result != "" {
		t.Error("LcFirst on empty should return empty")
	}
}

func TestOfWithInteger(t *testing.T) {
	// Test the 'Of' function with integer
	s := Of(123)
	if s.String() != "123" {
		t.Error("Of with integer should convert to string")
	}
}

func TestBasenameWithSuffix(t *testing.T) {
	result := Of("/path/to/file.txt").Basename(".txt").String()
	if result != "file" {
		t.Errorf("Basename with suffix = %q", result)
	}
}

// TestRegexCache_LRUEviction inserts more than regexCacheMax distinct
// patterns and confirms the cache stays bounded.
func TestRegexCache_LRUEviction(t *testing.T) {
	regexCache.clear()

	const overflow = regexCacheMax + 250
	for i := 0; i < overflow; i++ {
		// Each pattern is unique. Use a literal that compiles cheaply.
		pattern := "^test" + Random_forCacheKey(i) + "$"
		if _, err := getRegexE(pattern); err != nil {
			t.Fatalf("getRegexE(%q) error: %v", pattern, err)
		}
	}

	if got := regexCache.len(); got > regexCacheMax {
		t.Errorf("cache size = %d; want <= %d", got, regexCacheMax)
	}
}

// Random_forCacheKey produces a unique-enough deterministic suffix for the
// LRU eviction test. We avoid using Random() so we do not consume entropy.
func Random_forCacheKey(i int) string {
	// Cheap base36-ish encoding.
	const alpha = "abcdefghijklmnopqrstuvwxyz0123456789"
	if i == 0 {
		return string(alpha[0])
	}
	var out []byte
	for i > 0 {
		out = append(out, alpha[i%len(alpha)])
		i /= len(alpha)
	}
	return string(out)
}

func TestMatchSafe_ValidPattern(t *testing.T) {
	ok, err := MatchSafe(`^abc$`, "abc")
	if err != nil {
		t.Fatalf("MatchSafe error: %v", err)
	}
	if !ok {
		t.Error("MatchSafe(^abc$, abc) = false; want true")
	}
}

func TestMatchSafe_MalformedPatternReturnsError(t *testing.T) {
	// "(unclosed" is malformed: open paren with no close.
	ok, err := MatchSafe("(unclosed", "abc")
	if err == nil {
		t.Fatalf("MatchSafe with malformed pattern: want error, got ok=%v", ok)
	}
	if ok {
		t.Errorf("MatchSafe with malformed pattern returned true")
	}
}

func TestMatchAllSafe_MalformedPatternReturnsError(t *testing.T) {
	got, err := MatchAllSafe("(unclosed", "abc")
	if err == nil {
		t.Fatalf("MatchAllSafe with malformed pattern: want error, got %v", got)
	}
	if got != nil {
		t.Errorf("MatchAllSafe with malformed pattern: want nil, got %v", got)
	}
}

func TestTestSafe_MalformedPatternReturnsError(t *testing.T) {
	if _, err := TestSafe("[", "x"); err == nil {
		t.Fatal("TestSafe with malformed pattern: want error, got nil")
	}
}

func TestIsSafe_MalformedPatternReturnsError(t *testing.T) {
	// "(" becomes "^(.$" after glob escaping, which is still malformed.
	if _, err := IsSafe("(", "x"); err == nil {
		t.Fatal("IsSafe with malformed pattern: want error, got nil")
	}
}

func TestIsSafe_ValidPatternMatches(t *testing.T) {
	// Is uses glob syntax: * matches any sequence.
	ok, err := IsSafe("foo*", "foo123")
	if err != nil {
		t.Fatalf("IsSafe error: %v", err)
	}
	if !ok {
		t.Error("IsSafe(foo*, foo123) = false; want true")
	}
}

func TestMarkdown_EscapesHeaderContent(t *testing.T) {
	got := Markdown("# <script>alert(1)</script>")
	if strings.Contains(got, "<script>") {
		t.Errorf("Markdown leaked raw <script> tag: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("Markdown did not escape <script>: %q", got)
	}
}

func TestMarkdown_EscapesBoldContent(t *testing.T) {
	got := Markdown("**<img onerror=alert(1)>**")
	if strings.Contains(got, "<img") {
		t.Errorf("Markdown leaked raw <img>: %q", got)
	}
	if !strings.Contains(got, "<strong>") {
		t.Errorf("Markdown lost <strong> wrapping: %q", got)
	}
}

func TestMarkdown_EscapesCodeContent(t *testing.T) {
	got := Markdown("`<b>x</b>`")
	if strings.Contains(got, "<b>") {
		t.Errorf("Markdown leaked raw <b> in code: %q", got)
	}
	if !strings.Contains(got, "<code>&lt;b&gt;") {
		t.Errorf("Markdown code did not escape: %q", got)
	}
}

func TestMarkdown_RejectsJavascriptLink(t *testing.T) {
	got := Markdown("[click](javascript:alert(1))")
	if strings.Contains(strings.ToLower(got), "javascript:") {
		t.Errorf("Markdown leaked javascript: URI: %q", got)
	}
	if strings.Contains(got, "<a ") {
		t.Errorf("Markdown rendered <a> tag for javascript: URI: %q", got)
	}
	// Link text must still be present (as escaped plain text).
	if !strings.Contains(got, "click") {
		t.Errorf("Markdown dropped link text: %q", got)
	}
}

func TestMarkdown_RejectsDataLink(t *testing.T) {
	got := Markdown("[x](data:text/html,<script>alert(1)</script>)")
	if strings.Contains(strings.ToLower(got), "data:") {
		t.Errorf("Markdown leaked data: URI: %q", got)
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("Markdown leaked <script>: %q", got)
	}
}

func TestMarkdown_RejectsVbscriptLink(t *testing.T) {
	got := Markdown("[x](vbscript:msgbox)")
	if strings.Contains(strings.ToLower(got), "vbscript:") {
		t.Errorf("Markdown leaked vbscript: URI: %q", got)
	}
}

func TestMarkdown_AttributeInjectionEscaped(t *testing.T) {
	// Attacker tries to break out of href with quote injection.
	got := Markdown(`[x]("onclick=alert(1) ")`)
	// The literal sequence must not become a real attribute.
	if strings.Contains(got, "onclick=alert") {
		t.Errorf("Markdown allowed attribute injection: %q", got)
	}
	// The whole URL is not in the allowlist so the link is dropped to text.
	if strings.Contains(got, "<a ") {
		t.Errorf("Markdown rendered <a> for unsafe URL: %q", got)
	}
}

func TestMarkdown_AllowsHttpsLink(t *testing.T) {
	got := Markdown("[home](https://example.com)")
	if !strings.Contains(got, `<a href="https://example.com">home</a>`) {
		t.Errorf("Markdown https link rendering wrong: %q", got)
	}
}

func TestMarkdown_AllowsRelativeLink(t *testing.T) {
	cases := []string{"/about", "#section", "./next", "../up"}
	for _, c := range cases {
		got := Markdown("[x](" + c + ")")
		if !strings.Contains(got, `<a href="`+c+`">x</a>`) {
			t.Errorf("Markdown did not render relative link %q: got %q", c, got)
		}
	}
}

func TestMarkdown_AllowsMailtoLink(t *testing.T) {
	got := Markdown("[mail](mailto:a@b.com)")
	if !strings.Contains(got, `<a href="mailto:a@b.com">mail</a>`) {
		t.Errorf("Markdown mailto link rendering wrong: %q", got)
	}
}

func TestMarkdown_EscapesRawImg(t *testing.T) {
	got := Markdown("<img src=x onerror=alert(1)>")
	if strings.Contains(got, "<img") {
		t.Errorf("Markdown leaked raw <img>: %q", got)
	}
	if !strings.Contains(got, "&lt;img") {
		t.Errorf("Markdown did not escape <img>: %q", got)
	}
}

func TestMarkdown_EscapesRawScriptOutsideConstruct(t *testing.T) {
	got := Markdown("hello <script>alert(1)</script> world")
	if strings.Contains(got, "<script>") {
		t.Errorf("Markdown leaked raw <script>: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("Markdown did not escape <script>: %q", got)
	}
}

func TestMarkdown_EscapesBareAngleAndAmp(t *testing.T) {
	got := Markdown("a < b && b > c")
	want := "a &lt; b &amp;&amp; b &gt; c"
	if got != want {
		t.Errorf("Markdown(%q) = %q; want %q", "a < b && b > c", got, want)
	}
}

func TestMarkdown_MixesPlainTextAndMarkdown(t *testing.T) {
	// Raw HTML must be escaped while the markdown construct still renders.
	got := Markdown("**bold** <script>alert(1)</script>")
	if strings.Contains(got, "<script>") {
		t.Errorf("Markdown leaked <script> alongside markdown: %q", got)
	}
	if !strings.Contains(got, "<strong>bold</strong>") {
		t.Errorf("Markdown lost <strong> rendering: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("Markdown did not escape <script>: %q", got)
	}
}

func TestMarkdown_LinkAlongsideRawHTML(t *testing.T) {
	got := Markdown("[home](https://example.com) <img src=x>")
	if !strings.Contains(got, `<a href="https://example.com">home</a>`) {
		t.Errorf("Markdown lost link rendering: %q", got)
	}
	if strings.Contains(got, "<img") {
		t.Errorf("Markdown leaked <img>: %q", got)
	}
}

func TestMarkdown_ValidSanity(t *testing.T) {
	// Plain valid markdown round-trips into expected HTML.
	cases := map[string]string{
		"# Hello":           "<h1>Hello</h1>",
		"## Heading":        "<h2>Heading</h2>",
		"### H3":            "<h3>H3</h3>",
		"**bold**":          "<strong>bold</strong>",
		"__bold__":          "<strong>bold</strong>",
		"*italic*":          "<em>italic</em>",
		"_italic_":          "<em>italic</em>",
		"`code`":            "<code>code</code>",
		"[link](https://x)": `<a href="https://x">link</a>`,
	}
	for in, want := range cases {
		got := Markdown(in)
		if got != want {
			t.Errorf("Markdown(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestMatch_ExistingBehavior is a sanity check that Match still works for
// valid patterns after the cache refactor.
func TestMatch_ExistingBehavior(t *testing.T) {
	if !Match(`^hello$`, "hello") {
		t.Error("Match(^hello$, hello) = false; want true")
	}
	if MatchAll(`\d+`, "a1b22c333") == nil {
		t.Error("MatchAll returned nil for valid pattern")
	}
	if !Test(`world`, "hello world") {
		t.Error("Test(world, hello world) = false; want true")
	}
	if !Is("foo*", "foo123") {
		t.Error("Is(foo*, foo123) = false; want true")
	}
}

// noPanic runs fn and reports whether it panicked. Used by the malformed
// pattern tests below to confirm the non-Safe API no longer panics.
func noPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	fn()
}

func TestMatch_MalformedPatternReturnsFalse(t *testing.T) {
	var got bool
	noPanic(t, func() {
		got = Match("(unclosed", "abc")
	})
	if got {
		t.Errorf("Match with malformed pattern = true; want false")
	}
}

func TestMatchAll_MalformedPatternReturnsNil(t *testing.T) {
	var got [][]string
	noPanic(t, func() {
		got = MatchAll("[", "abc")
	})
	if got != nil {
		t.Errorf("MatchAll with malformed pattern = %v; want nil", got)
	}
}

func TestTest_MalformedPatternReturnsFalse(t *testing.T) {
	var got bool
	noPanic(t, func() {
		got = Test("(unclosed", "abc")
	})
	if got {
		t.Errorf("Test with malformed pattern = true; want false")
	}
}

func TestIs_MalformedPatternReturnsFalse(t *testing.T) {
	// A bare "(" becomes "^(.$" after glob-to-regex conversion, which is
	// malformed regex. Before this fix it panicked.
	var got bool
	noPanic(t, func() {
		got = Is("(", "x")
	})
	if got {
		t.Errorf("Is with malformed pattern = true; want false")
	}
}

func TestStringableReplaceMatches_MalformedPatternNoPanic(t *testing.T) {
	// Malformed pattern must not panic. The value is left unchanged.
	var got string
	noPanic(t, func() {
		got = Of("foo123").ReplaceMatches("(unclosed", "X").String()
	})
	if got != "foo123" {
		t.Errorf("Stringable.ReplaceMatches with malformed pattern = %q; want %q", got, "foo123")
	}
}

func TestStringableReplaceMatches_ValidPatternStillWorks(t *testing.T) {
	// Sanity: the happy path still works after the refactor.
	got := Of("foo123bar456").ReplaceMatches("[0-9]+", "X").String()
	if got != "fooXbarX" {
		t.Errorf("Stringable.ReplaceMatches = %q; want %q", got, "fooXbarX")
	}
}

func TestStringableReplaceMatchesSafe_MalformedPatternReturnsError(t *testing.T) {
	s := Of("foo123")
	err := s.ReplaceMatchesSafe("(unclosed", "X")
	if err == nil {
		t.Fatal("ReplaceMatchesSafe with malformed pattern: want error, got nil")
	}
	if s.String() != "foo123" {
		t.Errorf("ReplaceMatchesSafe with malformed pattern mutated value: got %q", s.String())
	}
}

func TestStringableReplaceMatchesSafe_ValidPatternReturnsNil(t *testing.T) {
	s := Of("foo123")
	if err := s.ReplaceMatchesSafe("[0-9]+", "X"); err != nil {
		t.Fatalf("ReplaceMatchesSafe error: %v", err)
	}
	if s.String() != "fooX" {
		t.Errorf("ReplaceMatchesSafe value = %q; want %q", s.String(), "fooX")
	}
}
