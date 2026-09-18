package str

import (
	"strings"
	"sync"
	"testing"
)

// caserCases pins the output of every function backed by the x/text title
// caser: Title, Headline (via Title), Camel and Studly. The expected values are
// literals captured from a single-threaded run rather than computed inside the
// test, so a concurrent assertion can never compare one corrupted result
// against another.
var caserCases = []struct {
	name     string
	in       string
	title    string
	headline string
	camel    string
	studly   string
}{
	{"empty", "", "", "", "", ""},
	{"whitespace only", "   ", "   ", "   ", "", ""},
	{"ascii word", "hello", "Hello", "Hello", "hello", "Hello"},
	{
		"ascii multi-word",
		"hello world from velocity",
		"Hello World From Velocity", "Hello World From Velocity",
		"helloWorldFromVelocity", "HelloWorldFromVelocity",
	},
	{"ascii upper", "HELLO WORLD", "Hello World", "Hello World", "helloWorld", "HelloWorld"},
	{
		"mixed delimiters",
		"user_profile-page.settings",
		"User_profile-Page.settings", "User Profile Page.settings",
		"userProfilePageSettings", "UserProfilePageSettings",
	},
	{
		"camel boundary",
		"listToolsHTTPHandler",
		"Listtoolshttphandler", "List Toolshttp Handler",
		"listtoolshttphandler", "Listtoolshttphandler",
	},
	{"apostrophe", "o'neil's book", "O'neil's Book", "O'neil's Book", "o'neil'sBook", "O'neil'sBook"},
	{
		"latin diacritics",
		"élan vital über straße",
		"Élan Vital Über Straße", "Élan Vital Über Straße",
		"élanVitalÜberStraße", "ÉlanVitalÜberStraße",
	},
	{"cyrillic", "привет мир", "Привет Мир", "Привет Мир", "приветМир", "ПриветМир"},
	// U+01C6/U+01C9 have a titlecase form distinct from their uppercase form.
	{"titlecase digraphs", "ǆungla ǉubav", "ǅungla ǈubav", "ǅungla ǈubav", "ǆunglaǈubav", "ǅunglaǈubav"},
	{"caseless script", "日本語 テキスト", "日本語 テキスト", "日本語 テキスト", "日本語テキスト", "日本語テキスト"},
	// Ligatures expand when title-cased, so the output outgrows the input.
	{"expanding ligatures", "ﬁne ﬂuid", "Fine Fluid", "Fine Fluid", "ﬁneFluid", "FineFluid"},
	{
		"long input",
		strings.Repeat("lorem ipsum ", 64),
		strings.Repeat("Lorem Ipsum ", 64), strings.Repeat("Lorem Ipsum ", 64),
		"lorem" + "Ipsum" + strings.Repeat("LoremIpsum", 63), strings.Repeat("LoremIpsum", 64),
	},
}

// TestCaserFunctions_Golden checks the pinned values single-threaded, so a
// failure in the concurrent test below is attributable to concurrency and not
// to a stale expectation.
func TestCaserFunctions_Golden(t *testing.T) {
	for _, tc := range caserCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Title(tc.in); got != tc.title {
				t.Errorf("Title(%q) = %q, want %q", tc.in, got, tc.title)
			}
			if got := Headline(tc.in); got != tc.headline {
				t.Errorf("Headline(%q) = %q, want %q", tc.in, got, tc.headline)
			}
			if got := Camel(tc.in); got != tc.camel {
				t.Errorf("Camel(%q) = %q, want %q", tc.in, got, tc.camel)
			}
			if got := Studly(tc.in); got != tc.studly {
				t.Errorf("Studly(%q) = %q, want %q", tc.in, got, tc.studly)
			}
		})
	}
}

// TestCaserFunctions_Concurrent is the regression test for the package-level
// cases.Caser that Title, Headline, Camel and Studly used to share. A Caser
// keeps transform state while it processes a string, so sharing one across
// goroutines is a data race that corrupts output and panics with
// "slice bounds out of range". Under -race this test fails against that code.
func TestCaserFunctions_Concurrent(t *testing.T) {
	const (
		goroutines = 16
		iterations = 200
	)

	var wg sync.WaitGroup
	start := make(chan struct{})

	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release every goroutine at once to maximise overlap

			for i := range iterations {
				// Offset by goroutine so different inputs are in flight together.
				tc := caserCases[(g+i)%len(caserCases)]

				if got := Title(tc.in); got != tc.title {
					t.Errorf("goroutine %d iter %d: Title(%q) = %q, want %q", g, i, tc.in, got, tc.title)
					return
				}
				if got := Headline(tc.in); got != tc.headline {
					t.Errorf("goroutine %d iter %d: Headline(%q) = %q, want %q", g, i, tc.in, got, tc.headline)
					return
				}
				if got := Camel(tc.in); got != tc.camel {
					t.Errorf("goroutine %d iter %d: Camel(%q) = %q, want %q", g, i, tc.in, got, tc.camel)
					return
				}
				if got := Studly(tc.in); got != tc.studly {
					t.Errorf("goroutine %d iter %d: Studly(%q) = %q, want %q", g, i, tc.in, got, tc.studly)
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()
}

var benchCaserInput = "user_profile-page settings for the velocity framework"

func BenchmarkTitle(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Title(benchCaserInput)
	}
}

func BenchmarkHeadline(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Headline(benchCaserInput)
	}
}

func BenchmarkStudly(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		Studly(benchCaserInput)
	}
}
