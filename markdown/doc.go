// Package markdown renders Markdown documents to HTML and exposes the
// structure a documentation site needs alongside the markup: front matter,
// title, a heading table of contents, a plain-text body for search indexing,
// and a word count.
//
// The engine is CommonMark with the GitHub extensions (tables,
// strikethrough, task lists, autolinks) plus footnotes and definition lists.
// Smart punctuation is deliberately off so code-heavy prose renders the
// characters it was written with. Output is safe by default: raw HTML in the
// source is stripped and links or images with javascript:, vbscript:, file:
// or non-image data: destinations lose their destination. [AllowHTML] lifts
// both restrictions for trusted input.
//
// A [Renderer] is built once and reused; it is safe for concurrent use and
// keeps no per-document state.
//
//	r := markdown.New()
//	doc, err := r.Render(src)
//	// doc.HTML, doc.Title, doc.TOC, doc.FrontMatter, doc.Plain, doc.Words
//
// # Directives for documentation
//
// Fenced containers group content under a named directive with an optional
// title. Unknown names render as a callout; "steps" turns the h3 headings in
// its body into an ordered list of steps:
//
//	:::note Heads up
//	Rendering is pure.
//	:::
//
//	:::steps
//	### Install
//	### Configure
//	:::
//
// Nest containers by giving the outer fence more colons than the inner one.
// [WithContainer] replaces the markup for one name.
//
// Fenced code blocks accept a title in the info string; the default markup
// wraps titled blocks in a figure. [WithCodeBlock] replaces the markup and
// [WithHighlighter] plugs in syntax highlighting (the markdown/chroma
// subpackage provides an adapter). [WithLinkRewrite] maps relative links,
// for example ../apps/deploy.md to /docs/apps/deploy. [WithHeading]
// replaces the heading markup; the default renders stable GitHub-style ids
// and an anchor on h2 through h6.
//
// # Sites
//
// [LoadFS] renders every Markdown file below a directory of an [io/fs.FS]
// (an embedded docs tree, typically) into a [Collection] that exposes the
// pages, a section tree, previous and next links, a search index, and
// llms.txt renderings. Ordering follows front matter weight: sections sort by
// the weight of their index page and pages by their own weight, lower first,
// and anything unweighted (no weight, or no index page) follows the weighted
// entries alphabetically. Root-level pages always form the first section.
//
// # Basic and inline modes
//
// [Basic] limits the engine to CommonMark plus the GitHub extensions with
// plain headings and code blocks, and [Inline] renders only inline syntax
// with no block wrappers. Both back the str.Markdown and str.InlineMarkdown
// helpers.
package markdown
