package chroma_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/markdown"
	"github.com/velocitykode/velocity/markdown/chroma"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name     string
		opts     []chroma.Option
		lang     string
		code     string
		wantOK   bool
		contains []string
		absent   []string
	}{
		{"go with classes", nil, "go", "x := 1\n", true, []string{`<span class="`, "x"}, []string{"<pre", "style="}},
		{"alias", nil, "golang", "x := 1\n", true, []string{`<span class="`}, nil},
		{"inline style", []chroma.Option{chroma.WithStyle("github")}, "go", "x := 1\n", true, []string{`style="`}, []string{`class="`}},
		{"unknown style falls back", []chroma.Option{chroma.WithStyle("no-such-style")}, "go", "x := 1\n", true, []string{`style="`}, nil},
		{"unknown language", nil, "not-a-language", "x\n", false, nil, nil},
		{"empty language", nil, "", "x\n", false, nil, nil},
		{"escapes html", nil, "html", "<b>&</b>\n", true, []string{"&lt;", "&amp;"}, []string{"<b>"}},
		{"nil option", []chroma.Option{nil}, "go", "x\n", true, []string{"<span"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hl := chroma.New(tt.opts...)
			got, ok := hl(tt.lang, tt.code)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v; want %v (output %q)", ok, tt.wantOK, got)
			}
			for _, s := range tt.contains {
				if !strings.Contains(string(got), s) {
					t.Errorf("output %q lacks %q", got, s)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(string(got), s) {
					t.Errorf("output %q must not contain %q", got, s)
				}
			}
		})
	}
}

func TestNew_WithRenderer(t *testing.T) {
	r := markdown.New(markdown.WithHighlighter(chroma.New()))
	doc, err := r.Render([]byte("```go\nfmt.Println(1)\n```\n\n```nope\nx\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(doc.HTML)
	if !strings.HasPrefix(html, `<pre><code class="language-go"><span`) {
		t.Errorf("highlighted block not wrapped in the markdown code markup: %s", html)
	}
	if !strings.Contains(html, "<pre><code class=\"language-nope\">x\n</code></pre>") {
		t.Errorf("unknown language should fall back to escaped source: %s", html)
	}
	if strings.Contains(html, "<pre><pre") {
		t.Errorf("chroma emitted its own <pre>: %s", html)
	}
}

func TestStylesheet(t *testing.T) {
	css, err := chroma.Stylesheet("github")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(css, ".chroma") || strings.Contains(css, "*/ .bg ") {
		t.Errorf("stylesheet still targets Chroma's own wrapper classes:\n%s", css)
	}
	if !strings.Contains(css, "/* PreWrapper */ pre {") {
		t.Errorf("stylesheet lacks the pre rule:\n%s", css)
	}
	for _, line := range strings.Split(strings.TrimSpace(css), "\n") {
		if _, rule, ok := strings.Cut(line, "*/ "); !ok || !strings.HasPrefix(rule, "pre") {
			t.Errorf("rule not scoped to pre: %q", line)
		}
	}
	if _, err := chroma.Stylesheet("no-such-style"); err == nil {
		t.Error("Stylesheet(unknown) returned nil error")
	}
}

// TestStylesheet_MatchesGeneratedMarkup pins that the classes New emits are
// the classes Stylesheet styles, inside the markup the markdown package
// wraps them in.
func TestStylesheet_MatchesGeneratedMarkup(t *testing.T) {
	css, err := chroma.Stylesheet("github")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := markdown.New(markdown.WithHighlighter(chroma.New())).Render([]byte("```go\nfunc main() {}\n```\n"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(doc.HTML)
	if !strings.HasPrefix(html, "<pre><code class=\"language-go\">") {
		t.Fatalf("unexpected wrapper: %s", html)
	}
	classes := regexp.MustCompile(`class="([a-z]+)"`).FindAllStringSubmatch(html, -1)
	styled := 0
	for _, m := range classes {
		if m[1] == "language" {
			continue
		}
		if strings.Contains(css, "pre ."+m[1]+" {") {
			styled++
		}
	}
	if styled == 0 {
		t.Errorf("no emitted token class has a rule in the stylesheet\nhtml: %s\ncss: %s", html, css)
	}
	for _, class := range []string{"kd", "nf"} {
		if !strings.Contains(html, `class="`+class+`"`) || !strings.Contains(css, "pre ."+class+" {") {
			t.Errorf("class %q must appear in both the markup and the stylesheet", class)
		}
	}
}
