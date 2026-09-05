package markdown

import (
	"bufio"
	"bytes"
	stdhtml "html"
	"strings"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

// analysis is what one pre-render walk over the AST yields: heading ids
// assigned on the nodes, the table of contents, the plain text and the
// first h1.
type analysis struct {
	toc   []Heading
	plain string
	h1    string
}

func (r *Renderer) analyze(root ast.Node, source []byte) analysis {
	ids := newSlugger()
	// Explicit ids win wherever they appear, so reserve every one before a
	// generated id can take its slug.
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && entering {
			if id := explicitID(h); id != "" {
				ids.reserve(id)
			}
		}
		return ast.WalkContinue, nil
	})

	var flat []Heading
	var h1 string
	pb := &plainBuilder{}

	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch v := n.(type) {
		case *ast.Heading:
			if !entering {
				return ast.WalkContinue, nil
			}
			txt := nodeText(v, source)
			id := explicitID(v)
			if id == "" {
				id = ids.unique(txt)
				v.SetAttributeString("id", []byte(id))
			}
			if v.Level == 1 && h1 == "" {
				h1 = txt
			}
			if v.Level >= r.cfg.tocMin && v.Level <= r.cfg.tocMax {
				flat = append(flat, Heading{ID: id, Level: v.Level, Text: txt})
			}
			pb.text(txt)
			pb.newline()
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			if entering {
				pb.text(inlineText(v.Segment.Value(source), v.IsRaw()))
				switch {
				case v.HardLineBreak():
					pb.newline()
				case v.SoftLineBreak():
					pb.space()
				}
			}
		case *ast.String:
			if entering {
				pb.text(inlineText(v.Value, v.IsRaw() || v.IsCode()))
			}
		case *ast.AutoLink:
			if entering {
				pb.text(string(v.Label(source)))
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			if entering {
				pb.text(blockLines(v, source))
				pb.newline()
				return ast.WalkSkipChildren, nil
			}
		case *ast.HTMLBlock, *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *containerNode:
			if entering && v.title != "" {
				pb.text(v.title)
				pb.newline()
			}
		case *east.TableCell:
			if !entering {
				pb.space()
			}
			return ast.WalkContinue, nil
		}
		if !entering && n.Type() == ast.TypeBlock {
			pb.newline()
		}
		return ast.WalkContinue, nil
	})

	return analysis{toc: buildTOC(flat), plain: pb.String(), h1: h1}
}

// plainBuilder accumulates text one block per line without doubled
// separators or trailing spaces.
type plainBuilder struct {
	buf []byte
}

func (b *plainBuilder) text(s string) {
	b.buf = append(b.buf, s...)
}

func (b *plainBuilder) space() {
	if n := len(b.buf); n > 0 && b.buf[n-1] != ' ' && b.buf[n-1] != '\n' {
		b.buf = append(b.buf, ' ')
	}
}

func (b *plainBuilder) newline() {
	b.buf = bytes.TrimRight(b.buf, " \t")
	if n := len(b.buf); n > 0 && b.buf[n-1] != '\n' {
		b.buf = append(b.buf, '\n')
	}
}

func (b *plainBuilder) String() string {
	return strings.TrimSpace(string(b.buf))
}

// nodeText returns the text content of a node's subtree without markup.
func nodeText(n ast.Node, source []byte) string {
	var b strings.Builder
	collectText(&b, n, source)
	return strings.TrimSpace(b.String())
}

func collectText(b *strings.Builder, n ast.Node, source []byte) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch v := c.(type) {
		case *ast.Text:
			b.WriteString(inlineText(v.Segment.Value(source), v.IsRaw()))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.WriteString(inlineText(v.Value, v.IsRaw() || v.IsCode()))
		case *ast.AutoLink:
			b.Write(v.Label(source))
		case *ast.RawHTML:
		default:
			collectText(b, c, source)
		}
	}
}

// buildTOC nests a flat, document-ordered heading list: each heading becomes
// a child of the nearest preceding heading with a smaller level.
func buildTOC(flat []Heading) []Heading {
	type node struct {
		heading Heading
		kids    []*node
	}
	root := &node{}
	stack := []*node{root}
	levels := []int{0}
	for _, h := range flat {
		for len(stack) > 1 && levels[len(levels)-1] >= h.Level {
			stack = stack[:len(stack)-1]
			levels = levels[:len(levels)-1]
		}
		n := &node{heading: h}
		parent := stack[len(stack)-1]
		parent.kids = append(parent.kids, n)
		stack = append(stack, n)
		levels = append(levels, h.Level)
	}
	var convert func(nodes []*node) []Heading
	convert = func(nodes []*node) []Heading {
		if len(nodes) == 0 {
			return nil
		}
		out := make([]Heading, len(nodes))
		for i, n := range nodes {
			out[i] = n.heading
			out[i].Children = convert(n.kids)
		}
		return out
	}
	return convert(root.kids)
}

// explicitID returns the id attribute set on a heading by {#id} syntax, or
// "" when the heading has none.
func explicitID(h *ast.Heading) string {
	if raw, ok := h.AttributeString("id"); ok {
		return attrString(raw)
	}
	return ""
}

// inlineText returns the character content of a text segment. Backslash
// escapes and entity references resolve exactly as the HTML renderer
// resolves them: the segment is written through the renderer's text writer
// and its HTML escaping is undone. Raw text (code spans) is returned as
// written.
func inlineText(value []byte, raw bool) string {
	if raw || !bytes.ContainsAny(value, "\\&") {
		return string(value)
	}
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	ghtml.DefaultWriter.Write(w, value)
	_ = w.Flush()
	return stdhtml.UnescapeString(buf.String())
}
