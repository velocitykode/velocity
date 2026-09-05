package markdown_test

import (
	"fmt"
	"html/template"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

func render(t *testing.T, src string, opts ...markdown.Option) *markdown.Document {
	t.Helper()
	doc, err := markdown.New(opts...).Render([]byte(src))
	if err != nil {
		t.Fatalf("Render(%q): %v", src, err)
	}
	return doc
}

func TestRender_HTML(t *testing.T) {
	tests := []struct {
		name string
		src  string
		opts []markdown.Option
		want string
	}{
		{"paragraph", "Hello *world*", nil, "<p>Hello <em>world</em></p>\n"},
		{"heading id and anchor", "## Install now", nil,
			`<h2 id="install-now">Install now<a class="anchor" href="#install-now" aria-label="Link to this section">#</a></h2>` + "\n"},
		{"h1 has id but no anchor", "# Title", nil, `<h1 id="title">Title</h1>` + "\n"},
		{"h6 anchor", "###### Deep", nil,
			`<h6 id="deep">Deep<a class="anchor" href="#deep" aria-label="Link to this section">#</a></h6>` + "\n"},
		{"explicit id", "## Custom {#my-id}", nil,
			`<h2 id="my-id">Custom<a class="anchor" href="#my-id" aria-label="Link to this section">#</a></h2>` + "\n"},
		{"hard wraps off", "a\nb", nil, "<p>a\nb</p>\n"},
		{"typographer off", `"quoted" -- it's...`, nil, "<p>&quot;quoted&quot; -- it's...</p>\n"},
		{"front matter stripped", "---\ntitle: T\n---\nBody", nil, "<p>Body</p>\n"},
		{"code fence without title", "```go\nx\n```", nil, "<pre><code class=\"language-go\">x\n</code></pre>\n"},
		{"code fence with title", "```go title=\"a.go\"\nx\n```", nil,
			"<figure class=\"code\"><figcaption>a.go</figcaption><pre><code class=\"language-go\">x\n</code></pre></figure>\n"},
		{"code title escaped", "```go title=\"<b>\"\nx\n```", nil,
			"<figure class=\"code\"><figcaption>&lt;b&gt;</figcaption><pre><code class=\"language-go\">x\n</code></pre></figure>\n"},
		{"code lang escaped", "```g<o\nx\n```", nil, "<pre><code class=\"language-g&lt;o\">x\n</code></pre>\n"},
		{"basic heading", "## Install", []markdown.Option{markdown.Basic()}, "<h2>Install</h2>\n"},
		{"basic code title ignored", "```go title=\"a.go\"\nx\n```", []markdown.Option{markdown.Basic()},
			"<pre><code class=\"language-go\">x\n</code></pre>\n"},
		{"basic keeps front matter as markdown", "---\nx\n---\n", []markdown.Option{markdown.Basic()}, "<hr>\n<h2>x</h2>\n"},
		{"inline no paragraph", "**b**", []markdown.Option{markdown.Inline()}, "<strong>b</strong>"},
		{"inline paragraphs joined by newline", "a\n\nb", []markdown.Option{markdown.Inline()}, "a\nb"},
		{"inline block syntax literal", "# h\n> q\n- l", []markdown.Option{markdown.Inline()}, "# h\n&gt; q\n- l"},
		{"inline indented text kept", "    x", []markdown.Option{markdown.Inline()}, "x"},
		{"inline image", "![a](/p.png)", []markdown.Option{markdown.Inline()}, `<img src="/p.png" alt="a">`},
		{"inline unsafe link dropped", "[x](javascript:alert(1))", []markdown.Option{markdown.Inline()}, "<a>x</a>"},
		{"inline raw html stripped", "<b>x</b>", []markdown.Option{markdown.Inline()}, "x"},
		{"inline allow html", "<b>x</b>", []markdown.Option{markdown.Inline(), markdown.AllowHTML()}, "<b>x</b>"},
		{"empty source", "", nil, ""},
		{"nil option ignored", "x", []markdown.Option{nil}, "<p>x</p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(render(t, tt.src, tt.opts...).HTML); got != tt.want {
				t.Errorf("HTML = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_SafetyDefaults(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"inline raw html stripped", "a <span onclick=x>b</span> c", "<p>a b c</p>\n"},
		{"script stripped", "<script>alert(1)</script>", ""},
		{"html block stripped", "<div>\nx\n</div>", ""},
		{"comment stripped", "<!-- hidden -->", ""},
		{"javascript link", "[x](javascript:alert(1))", "<p><a>x</a></p>\n"},
		{"javascript link mixed case", "[x](JaVaScRiPt:alert(1))", "<p><a>x</a></p>\n"},
		{"javascript link with leading whitespace entity", "[x](javascript&colon;alert(1))", "<p><a>x</a></p>\n"},
		{"vbscript link", "[x](vbscript:msgbox)", "<p><a>x</a></p>\n"},
		{"file link", "[x](file:///etc/passwd)", "<p><a>x</a></p>\n"},
		{"data link", "[x](data:text/html;base64,AA==)", "<p><a>x</a></p>\n"},
		{"data image link allowed", "[x](data:image/png;base64,AA==)", "<p><a href=\"data:image/png;base64,AA==\">x</a></p>\n"},
		{"unsafe image", "![x](javascript:alert(1))", "<p><img alt=\"x\"></p>\n"},
		{"unsafe autolink", "<javascript:alert(1)>", "<p><a>javascript:alert(1)</a></p>\n"},
		{"safe autolink", "<https://vel.build>", "<p><a href=\"https://vel.build\">https://vel.build</a></p>\n"},
		{"email autolink", "<a@b.co>", "<p><a href=\"mailto:a@b.co\">a@b.co</a></p>\n"},
		{"quote in destination escaped", `[x](/p?q="onclick=alert(1))`, "<p><a href=\"/p?q=%22onclick=alert(1)\">x</a></p>\n"},
		{"attribute breakout is text", `[x]("onclick=alert(1) ")`, "<p>[x](&quot;onclick=alert(1) &quot;)</p>\n"},
		{"code span escaped", "`<b>`", "<p><code>&lt;b&gt;</code></p>\n"},
		{"code block escaped", "```\n<script>\n```", "<pre><code>&lt;script&gt;\n</code></pre>\n"},
		{"title escaped", `[x](/p "a<b")`, "<p><a href=\"/p\" title=\"a&lt;b\">x</a></p>\n"},
		{"bare angle and ampersand", "a < b && c", "<p>a &lt; b &amp;&amp; c</p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(render(t, tt.src).HTML); got != tt.want {
				t.Errorf("HTML = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_AllowHTML(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"inline raw html", "a <span>b</span>", "<p>a <span>b</span></p>\n"},
		{"html block", "<div>\nx\n</div>\n", "<div>\nx\n</div>\n"},
		{"javascript link kept", "[x](javascript:alert(1))", "<p><a href=\"javascript:alert(1)\">x</a></p>\n"},
		{"unsafe image kept", "![x](data:text/html,x)", "<p><img src=\"data:text/html,x\" alt=\"x\"></p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(render(t, tt.src, markdown.AllowHTML()).HTML); got != tt.want {
				t.Errorf("HTML = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_Title(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"front matter title", "---\ntitle: Meta\n---\n# Body", "Meta"},
		{"empty front matter title falls back", "---\ntitle: \"\"\n---\n# Body", "Body"},
		{"non-string front matter title falls back", "---\ntitle: 3\n---\n# Body", "Body"},
		{"first h1", "## Two\n# One\n# Later", "One"},
		{"h1 markup stripped", "# `code` *and* [link](/x)", "code and link"},
		{"no title", "Just text", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := render(t, tt.src).Title; got != tt.want {
				t.Errorf("Title = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_FrontMatter(t *testing.T) {
	doc := render(t, "---\ntitle: T\nweight: 2\ntags: [a, b]\nnested:\n  k: v\n---\nx")
	want := map[string]any{"title": "T", "weight": 2, "tags": []any{"a", "b"}, "nested": map[string]any{"k": "v"}}
	if !reflect.DeepEqual(doc.FrontMatter, want) {
		t.Errorf("FrontMatter = %#v; want %#v", doc.FrontMatter, want)
	}
	if doc := render(t, "x"); doc.FrontMatter == nil || len(doc.FrontMatter) != 0 {
		t.Errorf("FrontMatter without block = %#v; want empty map", doc.FrontMatter)
	}
	if _, err := markdown.New().Render([]byte("---\ntitle: [\n---\nx")); err == nil {
		t.Error("Render() with malformed front matter returned nil error")
	}
}

func TestRender_TOC(t *testing.T) {
	src := "# Title\n## A\n### A1\n#### A1a\n### A2\n## B\n### B1\n# Another"
	tests := []struct {
		name string
		opts []markdown.Option
		want []markdown.Heading
	}{
		{"default h2 to h3", nil, []markdown.Heading{
			{ID: "a", Level: 2, Text: "A", Children: []markdown.Heading{
				{ID: "a1", Level: 3, Text: "A1"},
				{ID: "a2", Level: 3, Text: "A2"},
			}},
			{ID: "b", Level: 2, Text: "B", Children: []markdown.Heading{
				{ID: "b1", Level: 3, Text: "B1"},
			}},
		}},
		{"h1 to h4", []markdown.Option{markdown.TOCLevels(1, 4)}, []markdown.Heading{
			{ID: "title", Level: 1, Text: "Title", Children: []markdown.Heading{
				{ID: "a", Level: 2, Text: "A", Children: []markdown.Heading{
					{ID: "a1", Level: 3, Text: "A1", Children: []markdown.Heading{
						{ID: "a1a", Level: 4, Text: "A1a"},
					}},
					{ID: "a2", Level: 3, Text: "A2"},
				}},
				{ID: "b", Level: 2, Text: "B", Children: []markdown.Heading{
					{ID: "b1", Level: 3, Text: "B1"},
				}},
			}},
			{ID: "another", Level: 1, Text: "Another"},
		}},
		{"h3 only", []markdown.Option{markdown.TOCLevels(3, 3)}, []markdown.Heading{
			{ID: "a1", Level: 3, Text: "A1"},
			{ID: "a2", Level: 3, Text: "A2"},
			{ID: "b1", Level: 3, Text: "B1"},
		}},
		{"clamped and swapped", []markdown.Option{markdown.TOCLevels(9, 0)}, nil},
		{"max raised to min", []markdown.Option{markdown.TOCLevels(2, 1)}, []markdown.Heading{
			{ID: "a", Level: 2, Text: "A"},
			{ID: "b", Level: 2, Text: "B"},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, src, tt.opts...).TOC
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TOC = %+v; want %+v", got, tt.want)
			}
		})
	}
}

func TestRender_TOC_SkippedLevelNestsUnderNearest(t *testing.T) {
	got := render(t, "## A\n#### Deep\n## B", markdown.TOCLevels(2, 4)).TOC
	want := []markdown.Heading{
		{ID: "a", Level: 2, Text: "A", Children: []markdown.Heading{{ID: "deep", Level: 4, Text: "Deep"}}},
		{ID: "b", Level: 2, Text: "B"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("TOC = %+v; want %+v", got, want)
	}
}

func TestRender_HeadingIDs(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{"duplicates suffixed", "## A\n## A\n## A\n## A-1", []string{"a", "a-1", "a-2", "a-1-1"}},
		{"explicit id reserved", "## Custom {#a}\n## A", []string{"a", "a-1"}},
		{"empty heading", "## \u200b\n## !!!", []string{"section", "section-1"}},
		{"across levels", "# X\n## X\n### X", []string{"x", "x-1", "x-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			html := string(render(t, tt.src, markdown.TOCLevels(1, 6)).HTML)
			for _, h := range render(t, tt.src, markdown.TOCLevels(1, 6)).TOC {
				got = append(got, flattenIDs(h)...)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ids = %v; want %v", got, tt.want)
			}
			for _, id := range tt.want {
				if !strings.Contains(html, fmt.Sprintf(`id="%s"`, id)) {
					t.Errorf("HTML lacks id=%q: %s", id, html)
				}
			}
		})
	}
}

func TestRender_ExplicitIDsWin(t *testing.T) {
	doc := render(t, "## Foo\n## Bar {#foo}\n## Foo\n## Baz {#foo-1}", markdown.TOCLevels(2, 2))
	var ids []string
	for _, h := range doc.TOC {
		ids = append(ids, h.ID)
	}
	want := []string{"foo-2", "foo", "foo-3", "foo-1"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v; want %v (explicit ids reserved before any generated one)", ids, want)
	}
	if n := strings.Count(string(doc.HTML), `id="foo"`); n != 1 {
		t.Errorf("id=\"foo\" rendered %d times; want 1:\n%s", n, doc.HTML)
	}
}

func TestRender_TextUnescapes(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		title string
		plain string
		html  string
	}{
		{"entities and escapes in heading", "# A &amp; B \\*x\\* &copy; &#35;", "A & B *x* © #", "A & B *x* © #",
			`<h1 id="a--b-x--">A &amp; B *x* © #</h1>` + "\n"},
		{"code span keeps entities literal", "# `&amp;` and \\`", "&amp; and `", "&amp; and `",
			`<h1 id="amp-and-">` + "<code>&amp;amp;</code> and `</h1>\n"},
		{"image alt resolved once", "![A &amp; B \\*c\\*](/x.png)", "", "A & B *c*",
			`<p><img src="/x.png" alt="A &amp; B *c*"></p>` + "\n"},
		{"paragraph plain text", "Fish &amp; chips\\!", "", "Fish & chips!", "<p>Fish &amp; chips!</p>\n"},
		{"unknown entity stays", "Tom &nosuch; Jerry", "", "Tom &nosuch; Jerry", "<p>Tom &amp;nosuch; Jerry</p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := render(t, tt.src)
			if doc.Title != tt.title {
				t.Errorf("Title = %q; want %q", doc.Title, tt.title)
			}
			if doc.Plain != tt.plain {
				t.Errorf("Plain = %q; want %q", doc.Plain, tt.plain)
			}
			if string(doc.HTML) != tt.html {
				t.Errorf("HTML = %q; want %q", doc.HTML, tt.html)
			}
		})
	}
}

func flattenIDs(h markdown.Heading) []string {
	out := []string{h.ID}
	for _, c := range h.Children {
		out = append(out, flattenIDs(c)...)
	}
	return out
}

func TestRender_Plain(t *testing.T) {
	tests := []struct {
		name  string
		src   string
		want  string
		words int
	}{
		{"markup stripped", "# Title\n\nSome *emphasis* and `code` with [a link](/x).", "Title\nSome emphasis and code with a link.", 8},
		{"soft break is space, hard break is newline", "line one\nline two  \nline three", "line one line two\nline three", 6},
		{"raw html dropped", "a <b>b</b> c\n\n<div>\nblock\n</div>", "a b c", 3},
		{"code block kept", "```go\nx := 1\n```", "x := 1", 3},
		{"lists one item per line", "- one\n- two\n  - nested", "one\ntwo\nnested", 3},
		{"table rows", "| a | b |\n|---|---|\n| 1 | 2 |", "a b\n1 2", 4},
		{"container title and body", ":::note Heads up\nBody.\n:::", "Heads up\nBody.", 3},
		{"image alt kept", "![alt text](/x.png)", "alt text", 2},
		{"autolink label", "see https://vel.build now", "see https://vel.build now", 3},
		{"footnote", "Text[^1].\n\n[^1]: Note body.", "Text.\nNote body.", 3},
		{"task list", "- [ ] todo\n- [x] done", "todo\ndone", 2},
		{"empty", "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := render(t, tt.src)
			if doc.Plain != tt.want {
				t.Errorf("Plain = %q; want %q", doc.Plain, tt.want)
			}
			if doc.Words != tt.words {
				t.Errorf("Words = %d; want %d", doc.Words, tt.words)
			}
		})
	}
}

func TestRender_WithHeading(t *testing.T) {
	var seen []markdown.Heading
	doc := render(t, "## Two *b*\n### Three {#x}", markdown.WithHeading(func(w io.Writer, h markdown.Heading, body template.HTML) {
		seen = append(seen, h)
		fmt.Fprintf(w, "<h%d class=\"custom\">%s</h%d>\n", h.Level, body, h.Level)
	}))
	want := "<h2 class=\"custom\">Two <em>b</em></h2>\n<h3 class=\"custom\">Three</h3>\n"
	if string(doc.HTML) != want {
		t.Errorf("HTML = %q; want %q", doc.HTML, want)
	}
	wantSeen := []markdown.Heading{{ID: "two-b", Level: 2, Text: "Two b"}, {ID: "x", Level: 3, Text: "Three"}}
	if !reflect.DeepEqual(seen, wantSeen) {
		t.Errorf("headings passed to hook = %+v; want %+v", seen, wantSeen)
	}
	// A nil hook restores the default markup.
	doc = render(t, "## A", markdown.WithHeading(nil))
	if !strings.Contains(string(doc.HTML), `class="anchor"`) {
		t.Errorf("WithHeading(nil) did not restore the default: %s", doc.HTML)
	}
}

func TestRender_WithCodeBlock(t *testing.T) {
	var blocks []markdown.CodeBlock
	hook := markdown.WithCodeBlock(func(w io.Writer, b markdown.CodeBlock) {
		blocks = append(blocks, b)
		fmt.Fprintf(w, "<x-code lang=%q title=%q>%s</x-code>\n", b.Lang, b.Title, b.Body)
	})
	src := "```go title=\"main.go\" copy\nfmt.Println(\"<\")\n```\n\n    indented\n\n```\nplain\n```\n"
	doc := render(t, src, hook)
	want := "<x-code lang=\"go\" title=\"main.go\">fmt.Println(&quot;&lt;&quot;)\n</x-code>\n" +
		"<x-code lang=\"\" title=\"\">indented\n</x-code>\n" +
		"<x-code lang=\"\" title=\"\">plain\n</x-code>\n"
	if string(doc.HTML) != want {
		t.Errorf("HTML = %q; want %q", doc.HTML, want)
	}
	if len(blocks) != 3 {
		t.Fatalf("hook called %d times; want 3", len(blocks))
	}
	first := blocks[0]
	if first.Code != "fmt.Println(\"<\")\n" {
		t.Errorf("Code = %q", first.Code)
	}
	if !reflect.DeepEqual(first.Attrs, map[string]string{"title": "main.go", "copy": ""}) {
		t.Errorf("Attrs = %#v", first.Attrs)
	}
	if blocks[1].Attrs != nil || blocks[2].Attrs != nil {
		t.Errorf("Attrs for blocks without info = %#v, %#v; want nil", blocks[1].Attrs, blocks[2].Attrs)
	}
}

func TestRender_WithHighlighter(t *testing.T) {
	var calls []string
	hl := func(lang, code string) (template.HTML, bool) {
		calls = append(calls, lang+":"+code)
		if lang == "skip" {
			return "", false
		}
		return template.HTML("<span class=\"hl\">" + template.HTMLEscapeString(code) + "</span>"), true
	}
	src := "```go\nx<y\n```\n\n```skip\nz\n```\n\n```\nnolang\n```\n"
	doc := render(t, src, markdown.WithHighlighter(hl))
	want := "<pre><code class=\"language-go\"><span class=\"hl\">x&lt;y\n</span></code></pre>\n" +
		"<pre><code class=\"language-skip\">z\n</code></pre>\n" +
		"<pre><code>nolang\n</code></pre>\n"
	if string(doc.HTML) != want {
		t.Errorf("HTML = %q; want %q", doc.HTML, want)
	}
	if !reflect.DeepEqual(calls, []string{"go:x<y\n", "skip:z\n"}) {
		t.Errorf("highlighter calls = %q; want language-bearing fences only", calls)
	}
}

func TestRender_WithLinkRewrite(t *testing.T) {
	rewrite := markdown.WithLinkRewrite(func(href string) string {
		switch href {
		case "../apps/deploy.md":
			return "/docs/apps/deploy"
		case "launder":
			return "javascript:alert(1)"
		case "/img.png":
			return "/static/img.png"
		}
		return href
	})
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"relative link mapped", "[d](../apps/deploy.md)", "<p><a href=\"/docs/apps/deploy\">d</a></p>\n"},
		{"image mapped", "![i](/img.png)", "<p><img src=\"/static/img.png\" alt=\"i\"></p>\n"},
		{"unsafe input dropped before rewrite", "[x](javascript:alert(1))", "<p><a>x</a></p>\n"},
		{"unsafe rewrite output dropped", "[x](launder)", "<p><a>x</a></p>\n"},
		{"autolink untouched", "<https://vel.build/launder>", "<p><a href=\"https://vel.build/launder\">https://vel.build/launder</a></p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(render(t, tt.src, rewrite).HTML); got != tt.want {
				t.Errorf("HTML = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_WithContainer(t *testing.T) {
	custom := markdown.WithContainer("Note", func(w io.Writer, title string, body template.HTML) {
		fmt.Fprintf(w, "<div class=\"note\" title=%q>%s</div>\n", title, body)
	})
	steps := markdown.WithContainer("steps", func(w io.Writer, title string, body template.HTML) {
		fmt.Fprintf(w, "<ol class=\"mine\" data-title=%q>%s</ol>\n", title, body)
	})
	tests := []struct {
		name string
		src  string
		opts []markdown.Option
		want string
	}{
		{"custom callout by name", ":::note Hi\nBody\n:::", []markdown.Option{custom},
			"<div class=\"note\" title=\"Hi\"><p>Body</p>\n</div>\n"},
		{"other names keep default", ":::tip\nBody\n:::", []markdown.Option{custom},
			"<aside class=\"callout\" data-kind=\"tip\"><p class=\"callout-label\">Tip</p><p>Body</p>\n</aside>\n"},
		{"steps override gets li items", ":::steps Go\n### One\nA\n### Two\n:::", []markdown.Option{steps},
			"<ol class=\"mine\" data-title=\"Go\"><li><h3 id=\"one\">One<a class=\"anchor\" href=\"#one\" aria-label=\"Link to this section\">#</a></h3>\n<p>A</p>\n</li>\n" +
				"<li><h3 id=\"two\">Two<a class=\"anchor\" href=\"#two\" aria-label=\"Link to this section\">#</a></h3>\n</li>\n</ol>\n"},
		{"nil restores default", ":::note\nBody\n:::", []markdown.Option{custom, markdown.WithContainer("note", nil)},
			"<aside class=\"callout\" data-kind=\"note\"><p class=\"callout-label\">Note</p><p>Body</p>\n</aside>\n"},
		{"name case-insensitive in source", ":::NOTE\nBody\n:::", []markdown.Option{custom},
			"<div class=\"note\" title=\"\"><p>Body</p>\n</div>\n"},
		{"blank name ignored", ":::note\nBody\n:::", []markdown.Option{markdown.WithContainer("  ", nil)},
			"<aside class=\"callout\" data-kind=\"note\"><p class=\"callout-label\">Note</p><p>Body</p>\n</aside>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(render(t, tt.src, tt.opts...).HTML); got != tt.want {
				t.Errorf("HTML = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestRender_ContainerTOCAndBasic(t *testing.T) {
	doc := render(t, "## Intro\n:::steps\n### One\n### Two\n:::")
	want := []markdown.Heading{{ID: "intro", Level: 2, Text: "Intro", Children: []markdown.Heading{
		{ID: "one", Level: 3, Text: "One"}, {ID: "two", Level: 3, Text: "Two"},
	}}}
	if !reflect.DeepEqual(doc.TOC, want) {
		t.Errorf("TOC = %+v; want %+v", doc.TOC, want)
	}
	if got := string(render(t, ":::note\nx\n:::", markdown.Basic()).HTML); got != "<p>:::note\nx\n:::</p>\n" {
		t.Errorf("Basic() parsed a container: %q", got)
	}
}

func TestRenderer_Concurrent(t *testing.T) {
	r := markdown.New(markdown.WithHighlighter(func(lang, code string) (template.HTML, bool) {
		return template.HTML(template.HTMLEscapeString(code)), true
	}))
	src := []byte("---\ntitle: T\n---\n# T\n## A\n## A\n:::steps\n### S\n```go\nx\n```\n:::\nText[^1].\n\n[^1]: n\n")
	want, err := r.Render(src)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Render(src)
			if err != nil {
				errs <- err
				return
			}
			if !reflect.DeepEqual(got, want) {
				errs <- fmt.Errorf("concurrent render differs:\n%s\n%s", got.HTML, want.HTML)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func BenchmarkRender(b *testing.B) {
	r := markdown.New()
	src := []byte(strings.Repeat("## Heading\n\nSome *text* with `code` and a [link](/x).\n\n```go\nx := 1\n```\n\n:::note\nBody\n:::\n\n", 20))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := r.Render(src); err != nil {
			b.Fatal(err)
		}
	}
}
