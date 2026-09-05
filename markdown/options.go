package markdown

import (
	"html/template"
	"io"
	"strings"
)

// Option configures a Renderer. Options are applied in order by New.
type Option func(*config)

// ContainerFunc renders one container directive. title is the text that
// followed the directive name on the opening fence, or "" when absent, and
// body is the rendered HTML of the directive's content. For the "steps"
// directive body is the sequence of <li> items.
type ContainerFunc func(w io.Writer, title string, body template.HTML)

// CodeBlockFunc renders a fenced or indented code block.
type CodeBlockFunc func(w io.Writer, block CodeBlock)

// Highlighter returns the highlighted markup for the body of a code block.
// It reports false to fall back to the HTML-escaped source.
type Highlighter func(lang, code string) (template.HTML, bool)

// HeadingFunc renders a heading. body is the rendered inline HTML of the
// heading text; heading.Children is always empty here.
type HeadingFunc func(w io.Writer, heading Heading, body template.HTML)

// CodeBlock describes one code block to a CodeBlockFunc.
type CodeBlock struct {
	// Lang is the first word of the info string, "" for indented blocks or
	// fences without a language.
	Lang string
	// Title is the title="..." attribute of the info string, "" when absent.
	Title string
	// Attrs holds every key=value pair from the info string, nil when there
	// are none. Values may be bare, single-quoted or double-quoted.
	Attrs map[string]string
	// Code is the raw source of the block, trailing newline included.
	Code string
	// Body is the markup to place inside <code>: the highlighter output when
	// one applied, otherwise the HTML-escaped source.
	Body template.HTML
}

type config struct {
	allowHTML   bool
	basic       bool
	inline      bool
	tocMin      int
	tocMax      int
	containers  map[string]ContainerFunc
	codeBlock   CodeBlockFunc
	highlighter Highlighter
	linkRewrite func(href string) string
	heading     HeadingFunc
}

func defaultConfig() config {
	return config{tocMin: 2, tocMax: 3}
}

// AllowHTML passes raw HTML through and keeps javascript:, vbscript:, file:
// and data: destinations on links and images. Use it only for trusted input.
func AllowHTML() Option {
	return func(c *config) { c.allowHTML = true }
}

// Basic limits the engine to CommonMark plus the GitHub extensions (tables,
// strikethrough, task lists, autolinks). Headings render without ids or
// anchors, code blocks render as a bare <pre><code>, and front matter,
// container directives, code titles, footnotes and definition lists are not
// recognised. str.Markdown renders in this mode.
func Basic() Option {
	return func(c *config) { c.basic = true }
}

// Inline recognises inline syntax only (emphasis, code spans, links, images,
// autolinks, strikethrough) and renders no block wrappers: no <p>, and block
// syntax such as "# " or "- " is literal text. str.InlineMarkdown renders in
// this mode.
func Inline() Option {
	return func(c *config) { c.inline = true }
}

// TOCLevels sets the heading levels collected into Document.TOC, inclusive.
// The default is 2 through 3. Values are clamped to 1 through 6, and max is
// raised to min when it is smaller.
func TOCLevels(min, max int) Option {
	return func(c *config) {
		c.tocMin = clampLevel(min)
		c.tocMax = clampLevel(max)
		if c.tocMax < c.tocMin {
			c.tocMax = c.tocMin
		}
	}
}

func clampLevel(level int) int {
	if level < 1 {
		return 1
	}
	if level > 6 {
		return 6
	}
	return level
}

// WithContainer replaces the markup for the container directive called name
// (case-insensitive). A nil fn restores the default markup.
func WithContainer(name string, fn ContainerFunc) Option {
	return func(c *config) {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			return
		}
		if fn == nil {
			delete(c.containers, name)
			return
		}
		if c.containers == nil {
			c.containers = map[string]ContainerFunc{}
		}
		c.containers[name] = fn
	}
}

// WithCodeBlock replaces the markup for fenced and indented code blocks. A
// nil fn restores the default markup.
func WithCodeBlock(fn CodeBlockFunc) Option {
	return func(c *config) { c.codeBlock = fn }
}

// WithHighlighter installs a syntax highlighter for fenced code blocks that
// carry a language. The highlighter output replaces the escaped source inside
// <code>; when it reports false the escaped source is used.
func WithHighlighter(fn Highlighter) Option {
	return func(c *config) { c.highlighter = fn }
}

// WithLinkRewrite maps every link and image destination through fn before
// it is written, so a site can turn ../apps/deploy.md into /docs/apps/deploy.
// The safety checks run on the rewritten destination.
func WithLinkRewrite(fn func(href string) string) Option {
	return func(c *config) { c.linkRewrite = fn }
}

// WithHeading replaces the heading markup. A nil fn restores the default,
// which renders an id on every heading and an anchor link on h2 through h6.
func WithHeading(fn HeadingFunc) Option {
	return func(c *config) { c.heading = fn }
}
