package str

import (
	"html"
	"sync"

	"github.com/velocitykode/velocity/markdown"
)

var (
	markdownRenderer       = sync.OnceValue(func() *markdown.Renderer { return markdown.New(markdown.Basic()) })
	inlineMarkdownRenderer = sync.OnceValue(func() *markdown.Renderer { return markdown.New(markdown.Inline()) })
)

// Markdown converts GitHub Flavored Markdown to HTML: CommonMark plus tables,
// strikethrough, task lists and autolinks. Output is safe by default: raw
// HTML is stripped and links or images with javascript:, vbscript:, file: or
// non-image data: destinations lose their destination. Pass
// markdown.AllowHTML to trust the input, or any other markdown.Option to
// adjust rendering.
//
//	str.Markdown("# Velocity")  // "<h1>Velocity</h1>\n"
func Markdown(value string, opts ...markdown.Option) string {
	r := markdownRenderer()
	if len(opts) > 0 {
		r = markdown.New(append([]markdown.Option{markdown.Basic()}, opts...)...)
	}
	return renderMarkdown(r, value)
}

// InlineMarkdown converts inline Markdown to HTML without block wrappers:
// emphasis, code spans, links, images, autolinks and strikethrough render,
// nothing is wrapped in <p>, and block syntax is literal text. The same
// safety defaults as Markdown apply.
//
//	str.InlineMarkdown("**Velocity**")  // "<strong>Velocity</strong>"
func InlineMarkdown(value string, opts ...markdown.Option) string {
	r := inlineMarkdownRenderer()
	if len(opts) > 0 {
		r = markdown.New(append([]markdown.Option{markdown.Inline()}, opts...)...)
	}
	return renderMarkdown(r, value)
}

// renderMarkdown never fails for the basic and inline modes, which parse no
// front matter; the escaped source is the fallback should that ever change.
func renderMarkdown(r *markdown.Renderer, value string) string {
	doc, err := r.Render([]byte(value))
	if err != nil {
		return html.EscapeString(value)
	}
	return string(doc.HTML)
}

// Markdown converts the string as GitHub Flavored Markdown to HTML. See the
// package-level Markdown.
func (s *Stringable) Markdown(opts ...markdown.Option) *Stringable {
	s.value = Markdown(s.value, opts...)
	return s
}

// InlineMarkdown converts the string as inline Markdown to HTML without block
// wrappers. See the package-level InlineMarkdown.
func (s *Stringable) InlineMarkdown(opts ...markdown.Option) *Stringable {
	s.value = InlineMarkdown(s.value, opts...)
	return s
}
