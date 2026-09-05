package str

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

func TestMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"heading", "# Velocity", "<h1>Velocity</h1>\n"},
		{"headings without ids or anchors", "## Two\n### Three", "<h2>Two</h2>\n<h3>Three</h3>\n"},
		{"paragraph with inline markup", "**bold** _em_ `code`", "<p><strong>bold</strong> <em>em</em> <code>code</code></p>\n"},
		{"link", "[link](https://x)", "<p><a href=\"https://x\">link</a></p>\n"},
		{"bold in link label", "[**docs**](https://x)", "<p><a href=\"https://x\"><strong>docs</strong></a></p>\n"},
		{"relative link", "[x](../up)", "<p><a href=\"../up\">x</a></p>\n"},
		{"mailto link", "[mail](mailto:a@b.com)", "<p><a href=\"mailto:a@b.com\">mail</a></p>\n"},
		{"table", "| a |\n|---|\n| 1 |", "<table>\n<thead>\n<tr>\n<th>a</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n</tr>\n</tbody>\n</table>\n"},
		{"strikethrough", "~~x~~", "<p><del>x</del></p>\n"},
		{"task list", "- [x] done", "<ul>\n<li><input checked=\"\" disabled=\"\" type=\"checkbox\"> done</li>\n</ul>\n"},
		{"autolink", "see https://vel.build", "<p>see <a href=\"https://vel.build\">https://vel.build</a></p>\n"},
		{"code fence", "```go\nx\n```", "<pre><code class=\"language-go\">x\n</code></pre>\n"},
		{"code title ignored", "```go title=\"a.go\"\nx\n```", "<pre><code class=\"language-go\">x\n</code></pre>\n"},
		{"container is literal", ":::note\nx\n:::", "<p>:::note\nx\n:::</p>\n"},
		{"front matter is markdown", "---\ntitle: x\n---\n", "<hr>\n<h2>title: x</h2>\n"},
		{"typographer off", `"q" -- 'x'`, "<p>&quot;q&quot; -- 'x'</p>\n"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Markdown(tt.in); got != tt.want {
				t.Errorf("Markdown(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMarkdown_SafeByDefault(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"raw inline html stripped", "hello <script>alert(1)</script> world", "<p>hello alert(1) world</p>\n"},
		{"raw html block stripped", "<img src=x onerror=alert(1)>", ""},
		{"raw html in heading stripped", "# <script>alert(1)</script>", "<h1>alert(1)</h1>\n"},
		{"raw html in bold stripped", "**<img onerror=alert(1)>**", "<p><strong></strong></p>\n"},
		{"raw html in link label stripped", "[<script>x</script>](https://x)", "<p><a href=\"https://x\">x</a></p>\n"},
		{"code span escaped", "`<b>x</b>`", "<p><code>&lt;b&gt;x&lt;/b&gt;</code></p>\n"},
		{"bare angle and ampersand escaped", "a < b && b > c", "<p>a &lt; b &amp;&amp; b &gt; c</p>\n"},
		{"javascript link dropped", "[click](javascript:alert(1))", "<p><a>click</a></p>\n"},
		{"javascript link keeps label markup", "[**x**](javascript:alert(1))", "<p><a><strong>x</strong></a></p>\n"},
		{"data link dropped", "[x](data:text/html,<script>alert(1)</script>)", "<p><a>x</a></p>\n"},
		{"vbscript link dropped", "[x](vbscript:msgbox)", "<p><a>x</a></p>\n"},
		{"attribute injection is text", `[x]("onclick=alert(1) ")`, "<p>[x](&quot;onclick=alert(1) &quot;)</p>\n"},
		{"unsafe image dropped", "![x](javascript:alert(1))", "<p><img alt=\"x\"></p>\n"},
		{"link alongside raw html", "[home](https://example.com) <img src=x>", "<p><a href=\"https://example.com\">home</a> </p>\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Markdown(tt.in); got != tt.want {
				t.Errorf("Markdown(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMarkdown_Options(t *testing.T) {
	if got := Markdown("<b>x</b> [j](javascript:alert(1))", markdown.AllowHTML()); got != "<p><b>x</b> <a href=\"javascript:alert(1)\">j</a></p>\n" {
		t.Errorf("AllowHTML = %q", got)
	}
	rewrite := markdown.WithLinkRewrite(func(h string) string { return "/r" + h })
	if got := Markdown("[x](/a)", rewrite); got != "<p><a href=\"/r/a\">x</a></p>\n" {
		t.Errorf("WithLinkRewrite = %q", got)
	}
	// Options never turn the basic mode into the document mode.
	if got := Markdown("## H", rewrite); got != "<h2>H</h2>\n" {
		t.Errorf("heading with options = %q; want plain heading", got)
	}
}

func TestInlineMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bold", "**Velocity**", "<strong>Velocity</strong>"},
		{"mixed", "**bold** text", "<strong>bold</strong> text"},
		{"link and code", "[x](https://x) `c`", "<a href=\"https://x\">x</a> <code>c</code>"},
		{"strikethrough and autolink", "~~s~~ https://vel.build", "<del>s</del> <a href=\"https://vel.build\">https://vel.build</a>"},
		{"heading syntax is literal", "# Title", "# Title"},
		{"list syntax is literal", "- item", "- item"},
		{"blockquote syntax is literal", "> q", "&gt; q"},
		{"fence backticks form a code span", "```\nx\n```", "<code>x</code>"},
		{"paragraphs joined by newline", "a\n\nb", "a\nb"},
		{"indented text kept", "    x", "x"},
		{"raw html stripped", "<b>x</b> <script>y</script>", "x y"},
		{"unsafe link dropped", "[x](javascript:alert(1))", "<a>x</a>"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := InlineMarkdown(tt.in); got != tt.want {
				t.Errorf("InlineMarkdown(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
	if got := InlineMarkdown("<b>x</b>", markdown.AllowHTML()); got != "<b>x</b>" {
		t.Errorf("InlineMarkdown with AllowHTML = %q", got)
	}
}

func TestStringable_Markdown(t *testing.T) {
	if got := Of("# Hello").Markdown().String(); got != "<h1>Hello</h1>\n" {
		t.Errorf("Of().Markdown() = %q", got)
	}
	// Like every other fluent method, both mutate the receiver.
	s := Of("**bold**")
	if s.Markdown() != s || s.String() != "<p><strong>bold</strong></p>\n" {
		t.Errorf("Markdown() did not mutate the receiver: %q", s.String())
	}
	s = Of("**bold**")
	if s.InlineMarkdown() != s || s.String() != "<strong>bold</strong>" {
		t.Errorf("InlineMarkdown() did not mutate the receiver: %q", s.String())
	}
	if got := Of("**bold** text").InlineMarkdown().String(); got != "<strong>bold</strong> text" {
		t.Errorf("Of().InlineMarkdown() = %q", got)
	}
	if got := Of("<b>x</b>").InlineMarkdown(markdown.AllowHTML()).Upper().String(); got != "<B>X</B>" {
		t.Errorf("chained = %q", got)
	}
}

func TestMarkdown_Concurrent(t *testing.T) {
	done := make(chan string, 32)
	for i := 0; i < 32; i++ {
		go func() { done <- Markdown("# T\n\n**b**") + InlineMarkdown("*e*") }()
	}
	for i := 0; i < 32; i++ {
		if got := <-done; got != "<h1>T</h1>\n<p><strong>b</strong></p>\n<em>e</em>" {
			t.Errorf("concurrent result = %q", got)
		}
	}
	if !strings.Contains(Markdown("x"), "<p>") {
		t.Error("sanity")
	}
}
