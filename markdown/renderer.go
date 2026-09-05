package markdown

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Document is the result of rendering one Markdown source.
type Document struct {
	// HTML is the rendered body, front matter excluded.
	HTML template.HTML
	// FrontMatter is the decoded YAML front matter; an empty map when the
	// source has none.
	FrontMatter map[string]any
	// Title is the front matter "title" when it is a non-empty string,
	// otherwise the text of the first h1, otherwise "".
	Title string
	// TOC nests the headings whose level falls within TOCLevels (h2 and h3
	// by default) in document order.
	TOC []Heading
	// Words counts the whitespace-separated words of Plain.
	Words int
	// Plain is the text content without markup, one line per block: heading
	// text, paragraphs, list items, table rows and code, in document order.
	// It feeds search indexes and llms.txt.
	Plain string
}

// Heading is one entry of a table of contents.
type Heading struct {
	// ID is the anchor id rendered on the heading.
	ID string
	// Level is 1 through 6.
	Level int
	// Text is the heading text without markup.
	Text string
	// Children holds the headings nested under this one.
	Children []Heading
}

// Renderer renders Markdown sources. Build one with New and reuse it: it
// carries no per-document state and is safe for concurrent use.
type Renderer struct {
	cfg config
	md  goldmark.Markdown
}

// New builds a Renderer. Without options it renders documents with every
// extension on and safe output; see the Option constructors.
func New(opts ...Option) *Renderer {
	cfg := defaultConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	r := &Renderer{cfg: cfg}
	r.md = r.engine()
	return r
}

func (r *Renderer) engine() goldmark.Markdown {
	nodeRenderers := renderer.WithNodeRenderers(util.Prioritized(&nodeRenderer{r: r}, 100))
	if r.cfg.inline {
		p := parser.NewParser(
			parser.WithBlockParsers(util.Prioritized(inlineParagraphParser{parser.NewParagraphParser()}, 1000)),
			parser.WithInlineParsers(parser.DefaultInlineParsers()...),
			parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...),
		)
		return goldmark.New(
			goldmark.WithParser(p),
			goldmark.WithExtensions(extension.Strikethrough, extension.Linkify),
			goldmark.WithRendererOptions(nodeRenderers),
		)
	}
	exts := []goldmark.Extender{extension.Table, extension.Strikethrough, extension.TaskList, extension.Linkify}
	var parserOpts []parser.Option
	if !r.cfg.basic {
		exts = append(exts, extension.Footnote, extension.DefinitionList, containerExtension{})
		parserOpts = append(parserOpts, parser.WithAttribute())
	}
	return goldmark.New(
		goldmark.WithExtensions(exts...),
		goldmark.WithParserOptions(parserOpts...),
		goldmark.WithRendererOptions(nodeRenderers),
	)
}

// inlineParagraphParser is the paragraph parser with indented lines
// accepted, so an inline document that starts with four spaces still
// renders: with no other block parser registered, nothing else would claim
// the line.
type inlineParagraphParser struct {
	parser.BlockParser
}

func (inlineParagraphParser) CanAcceptIndentedLine() bool { return true }

// Render renders src. The only error is malformed front matter.
func (r *Renderer) Render(src []byte) (*Document, error) {
	doc, _, err := r.render(src)
	return doc, err
}

// render also returns the source body with the front matter removed.
func (r *Renderer) render(src []byte) (*Document, []byte, error) {
	meta := map[string]any{}
	body := src
	if !r.cfg.basic && !r.cfg.inline {
		m, b, err := ParseFrontMatter(src)
		if err != nil {
			return nil, nil, err
		}
		meta, body = m, b
	}

	root := r.md.Parser().Parse(text.NewReader(body))
	info := r.analyze(root, body)

	var buf bytes.Buffer
	if err := r.md.Renderer().Render(&buf, body, root); err != nil {
		return nil, nil, fmt.Errorf("velocity/markdown: render: %w", err)
	}

	title := info.h1
	if t, ok := meta["title"].(string); ok && t != "" {
		title = t
	}
	return &Document{
		HTML:        template.HTML(buf.String()),
		FrontMatter: meta,
		Title:       title,
		TOC:         info.toc,
		Words:       len(strings.Fields(info.plain)),
		Plain:       info.plain,
	}, body, nil
}
