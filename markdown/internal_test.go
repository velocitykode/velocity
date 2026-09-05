package markdown

import (
	"reflect"
	"testing"
)

func TestParseInfo(t *testing.T) {
	tests := []struct {
		name      string
		info      string
		withAttrs bool
		wantLang  string
		wantAttrs map[string]string
	}{
		{"empty", "", true, "", nil},
		{"language only", "go", true, "go", nil},
		{"padded language", "  go  ", true, "go", nil},
		{"double quoted title", `go title="main.go"`, true, "go", map[string]string{"title": "main.go"}},
		{"single quoted title", `go title='my file.go'`, true, "go", map[string]string{"title": "my file.go"}},
		{"bare value", "go title=main.go", true, "go", map[string]string{"title": "main.go"}},
		{"flag without value", "go copy", true, "go", map[string]string{"copy": ""}},
		{"several", `ts title="a.ts" highlight=2 readonly`, true, "ts",
			map[string]string{"title": "a.ts", "highlight": "2", "readonly": ""}},
		{"braces", `ts {title="a.ts"}`, true, "ts", map[string]string{"title": "a.ts"}},
		{"unterminated quote", `go title="open`, true, "go", map[string]string{"title": "open"}},
		{"empty key skipped", `go =x`, true, "go", nil},
		{"tabs", "go\ttitle=\"a\"\tcopy", true, "go", map[string]string{"title": "a", "copy": ""}},
		{"attrs off", `go title="main.go"`, false, "go", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lang, attrs := parseInfo(tt.info, tt.withAttrs)
			if lang != tt.wantLang {
				t.Errorf("lang = %q; want %q", lang, tt.wantLang)
			}
			if !reflect.DeepEqual(attrs, tt.wantAttrs) {
				t.Errorf("attrs = %#v; want %#v", attrs, tt.wantAttrs)
			}
		})
	}
}

func TestSplitDirective(t *testing.T) {
	tests := []struct {
		rest, name, title string
	}{
		{"note", "note", ""},
		{"note\n", "note", ""},
		{"Note Heads up  \n", "note", "Heads up"},
		{"  warning\tRead me", "warning", "Read me"},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.rest, func(t *testing.T) {
			name, title := splitDirective(tt.rest)
			if name != tt.name || title != tt.title {
				t.Errorf("splitDirective(%q) = %q, %q; want %q, %q", tt.rest, name, title, tt.name, tt.title)
			}
		})
	}
}

func TestValidContainerName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"note", true}, {"Note", true}, {"step-2", true}, {"a_b", true}, {"n1", true},
		{"", false}, {"1st", false}, {"-x", false}, {"na me", false}, {"nöte", false}, {"a.b", false},
	}
	for _, tt := range tests {
		if got := validContainerName(tt.name); got != tt.want {
			t.Errorf("validContainerName(%q) = %v; want %v", tt.name, got, tt.want)
		}
	}
}

func TestBuildTOC(t *testing.T) {
	flat := []Heading{
		{ID: "a", Level: 2}, {ID: "a1", Level: 3}, {ID: "deep", Level: 5}, {ID: "a2", Level: 3},
		{ID: "b", Level: 2}, {ID: "orphan", Level: 4},
	}
	want := []Heading{
		{ID: "a", Level: 2, Children: []Heading{
			{ID: "a1", Level: 3, Children: []Heading{{ID: "deep", Level: 5}}},
			{ID: "a2", Level: 3},
		}},
		{ID: "b", Level: 2, Children: []Heading{{ID: "orphan", Level: 4}}},
	}
	if got := buildTOC(flat); !reflect.DeepEqual(got, want) {
		t.Errorf("buildTOC = %+v; want %+v", got, want)
	}
	if got := buildTOC(nil); got != nil {
		t.Errorf("buildTOC(nil) = %+v; want nil", got)
	}
	// A first heading deeper than the ones after it sits at the top level.
	got := buildTOC([]Heading{{ID: "x", Level: 3}, {ID: "y", Level: 2}})
	if len(got) != 2 || got[0].ID != "x" || got[1].ID != "y" {
		t.Errorf("buildTOC deep-first = %+v", got)
	}
}

func TestSluggerUnique(t *testing.T) {
	s := newSlugger()
	s.reserve("a")
	got := []string{s.unique("A"), s.unique("a"), s.unique("A 1"), s.unique("b")}
	want := []string{"a-1", "a-2", "a-1-1", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unique sequence = %v; want %v", got, want)
	}
}

func TestWeightOf(t *testing.T) {
	tests := []struct {
		name string
		meta map[string]any
		want int
	}{
		{"absent", map[string]any{}, 0},
		{"int", map[string]any{"weight": 3}, 3},
		{"int64", map[string]any{"weight": int64(4)}, 4},
		{"float", map[string]any{"weight": 5.0}, 5},
		{"numeric string", map[string]any{"weight": " 6 "}, 6},
		{"bad string", map[string]any{"weight": "heavy"}, 0},
		{"other type", map[string]any{"weight": true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := weightOf(tt.meta); got != tt.want {
				t.Errorf("weightOf = %d; want %d", got, tt.want)
			}
		})
	}
}

func TestAttrString(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{[]byte("bytes"), "bytes"},
		{"string", "string"},
		{42, "42"},
	}
	for _, tt := range tests {
		if got := attrString(tt.in); got != tt.want {
			t.Errorf("attrString(%v) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestNodeText_InlineKinds(t *testing.T) {
	r := New()
	doc, err := r.Render([]byte("## See https://vel.build and <b>raw</b> text\nwrapped"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.TOC) != 1 || doc.TOC[0].Text != "See https://vel.build and raw text" {
		t.Errorf("heading text = %+v; want autolink label kept, raw html dropped", doc.TOC)
	}
	if doc.Plain != "See https://vel.build and raw text\nwrapped" {
		t.Errorf("Plain = %q", doc.Plain)
	}
}

func TestHumanize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"getting-started", "Getting Started"},
		{"api_reference", "Api Reference"},
		{"apps", "Apps"},
		{"", ""},
		{"éclair-recipes", "Éclair Recipes"},
	}
	for _, tt := range tests {
		if got := humanize(tt.in); got != tt.want {
			t.Errorf("humanize(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncateWords(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"cut on a word boundary here", 12, "cut on a"},
		{"nospaceanywhereatall", 5, "nospa"},
		{"ünïcödé words here", 8, "ünïcödé"},
	}
	for _, tt := range tests {
		if got := truncateWords(tt.in, tt.n); got != tt.want {
			t.Errorf("truncateWords(%q, %d) = %q; want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
