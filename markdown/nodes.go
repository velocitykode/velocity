package markdown

import (
	"bufio"
	"bytes"
	"fmt"
	"html/template"
	"io"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/util"
)

// nodeRenderer overrides the goldmark HTML renderer for the nodes whose
// markup the package owns: headings, links, images, raw HTML, code blocks,
// container directives and, in inline mode, paragraphs. It holds no
// per-document state.
type nodeRenderer struct {
	r *Renderer
}

func (nr *nodeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindHeading, nr.renderHeading)
	reg.Register(ast.KindLink, nr.renderLink)
	reg.Register(ast.KindAutoLink, nr.renderAutoLink)
	reg.Register(ast.KindImage, nr.renderImage)
	reg.Register(ast.KindRawHTML, nr.renderRawHTML)
	reg.Register(ast.KindHTMLBlock, nr.renderHTMLBlock)
	reg.Register(ast.KindFencedCodeBlock, nr.renderFencedCodeBlock)
	reg.Register(ast.KindCodeBlock, nr.renderCodeBlock)
	reg.Register(kindContainer, nr.renderContainer)
	if nr.r.cfg.inline {
		reg.Register(ast.KindParagraph, nr.renderInlineParagraph)
	}
}

// renderNodes renders nodes through the same engine into a buffer, so a hook
// receives finished markup for the content it wraps.
func (nr *nodeRenderer) renderNodes(source []byte, nodes ...ast.Node) (template.HTML, error) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	for _, n := range nodes {
		if err := nr.r.md.Renderer().Render(w, source, n); err != nil {
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (nr *nodeRenderer) renderChildren(source []byte, n ast.Node) (template.HTML, error) {
	var nodes []ast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		nodes = append(nodes, c)
	}
	return nr.renderNodes(source, nodes...)
}

func (nr *nodeRenderer) renderHeading(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Heading)
	body, err := nr.renderChildren(source, n)
	if err != nil {
		return ast.WalkStop, err
	}
	h := Heading{Level: n.Level, Text: nodeText(n, source)}
	if id, ok := n.AttributeString("id"); ok {
		h.ID = attrString(id)
	}
	fn := nr.r.cfg.heading
	switch {
	case fn != nil:
	case nr.r.cfg.basic:
		fn = basicHeading
	default:
		fn = defaultHeading
	}
	fn(w, h, body)
	return ast.WalkSkipChildren, nil
}

// defaultHeading renders <hN id="..."> with an anchor link on h2 through h6.
func defaultHeading(w io.Writer, h Heading, body template.HTML) {
	id := template.HTMLEscapeString(h.ID)
	var b strings.Builder
	fmt.Fprintf(&b, `<h%d id="%s">%s`, h.Level, id, body)
	if h.Level >= 2 {
		fmt.Fprintf(&b, `<a class="anchor" href="#%s" aria-label="Link to this section">#</a>`, id)
	}
	fmt.Fprintf(&b, "</h%d>\n", h.Level)
	_, _ = io.WriteString(w, b.String())
}

// basicHeading renders a bare <hN>.
func basicHeading(w io.Writer, h Heading, body template.HTML) {
	_, _ = fmt.Fprintf(w, "<h%d>%s</h%d>\n", h.Level, body, h.Level)
}

func (nr *nodeRenderer) renderLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if !entering {
		_, _ = w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("<a")
	nr.writeDestination(w, "href", n.Destination, true)
	if n.Title != nil {
		_, _ = w.WriteString(` title="`)
		html.DefaultWriter.Write(w, n.Title)
		_ = w.WriteByte('"')
	}
	if n.Attributes() != nil {
		html.RenderAttributes(w, n, html.LinkAttributeFilter)
	}
	_ = w.WriteByte('>')
	return ast.WalkContinue, nil
}

func (nr *nodeRenderer) renderAutoLink(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.AutoLink)
	url := n.URL(source)
	if n.AutoLinkType == ast.AutoLinkEmail && !bytes.HasPrefix(bytes.ToLower(url), []byte("mailto:")) {
		url = append([]byte("mailto:"), url...)
	}
	_, _ = w.WriteString("<a")
	nr.writeDestination(w, "href", url, false)
	if n.Attributes() != nil {
		html.RenderAttributes(w, n, html.LinkAttributeFilter)
	}
	_ = w.WriteByte('>')
	_, _ = w.Write(util.EscapeHTML(n.Label(source)))
	_, _ = w.WriteString("</a>")
	return ast.WalkContinue, nil
}

func (nr *nodeRenderer) renderImage(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	_, _ = w.WriteString("<img")
	nr.writeDestination(w, "src", n.Destination, true)
	_, _ = w.WriteString(` alt="`)
	_, _ = w.Write(util.EscapeHTML([]byte(nodeText(n, source))))
	_ = w.WriteByte('"')
	if n.Title != nil {
		_, _ = w.WriteString(` title="`)
		html.DefaultWriter.Write(w, n.Title)
		_ = w.WriteByte('"')
	}
	if n.Attributes() != nil {
		html.RenderAttributes(w, n, html.ImageAttributeFilter)
	}
	_ = w.WriteByte('>')
	return ast.WalkSkipChildren, nil
}

// writeDestination writes ` name="url"` after the link rewrite hook and the
// safety check. An unsafe destination is dropped, leaving the element
// without the attribute. The check runs on the destination as written and
// again on the rewritten one, so a rewrite can neither launder nor introduce
// an unsafe scheme.
func (nr *nodeRenderer) writeDestination(w util.BufWriter, name string, dest []byte, rewrite bool) {
	if !nr.r.cfg.allowHTML && html.IsDangerousURL(util.URLEscape(dest, true)) {
		return
	}
	if rewrite && nr.r.cfg.linkRewrite != nil {
		dest = []byte(nr.r.cfg.linkRewrite(string(dest)))
	}
	escaped := util.URLEscape(dest, true)
	if !nr.r.cfg.allowHTML && html.IsDangerousURL(escaped) {
		return
	}
	_ = w.WriteByte(' ')
	_, _ = w.WriteString(name)
	_, _ = w.WriteString(`="`)
	_, _ = w.Write(util.EscapeHTML(escaped))
	_ = w.WriteByte('"')
}

func (nr *nodeRenderer) renderRawHTML(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering || !nr.r.cfg.allowHTML {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.RawHTML)
	for i := 0; i < n.Segments.Len(); i++ {
		seg := n.Segments.At(i)
		_, _ = w.Write(seg.Value(source))
	}
	return ast.WalkSkipChildren, nil
}

func (nr *nodeRenderer) renderHTMLBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !nr.r.cfg.allowHTML {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.HTMLBlock)
	if entering {
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			html.DefaultWriter.SecureWrite(w, seg.Value(source))
		}
	} else if n.HasClosure() {
		html.DefaultWriter.SecureWrite(w, n.ClosureLine.Value(source))
	}
	return ast.WalkContinue, nil
}

func (nr *nodeRenderer) renderFencedCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.FencedCodeBlock)
	info := ""
	if n.Info != nil {
		info = string(n.Info.Segment.Value(source))
	}
	nr.writeCodeBlock(w, nr.r.codeBlock(info, blockLines(n, source)))
	return ast.WalkSkipChildren, nil
}

func (nr *nodeRenderer) renderCodeBlock(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	nr.writeCodeBlock(w, nr.r.codeBlock("", blockLines(node, source)))
	return ast.WalkSkipChildren, nil
}

func (nr *nodeRenderer) writeCodeBlock(w io.Writer, block CodeBlock) {
	fn := nr.r.cfg.codeBlock
	if fn == nil {
		fn = defaultCodeBlock
	}
	fn(w, block)
}

func (nr *nodeRenderer) renderContainer(w util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*containerNode)
	fn := nr.r.cfg.containers[n.name]
	if n.name == "steps" {
		lead, items, err := nr.renderSteps(source, n)
		if err != nil {
			return ast.WalkStop, err
		}
		_, _ = w.WriteString(string(lead))
		if items == "" {
			// No h3 headings: nothing to enumerate, so the content stands
			// as written rather than an empty list.
			return ast.WalkSkipChildren, nil
		}
		if fn == nil {
			fn = defaultSteps
		}
		fn(w, n.title, items)
		return ast.WalkSkipChildren, nil
	}
	body, err := nr.renderChildren(source, n)
	if err != nil {
		return ast.WalkStop, err
	}
	if fn == nil {
		fn = calloutFunc(n.name)
	}
	fn(w, n.title, body)
	return ast.WalkSkipChildren, nil
}

// renderSteps groups the body of a steps directive by its h3 headings. Content
// before the first h3 is returned as lead and rendered before the list; each
// h3 and the blocks that follow it become one <li>.
func (nr *nodeRenderer) renderSteps(source []byte, n *containerNode) (lead, items template.HTML, err error) {
	var leadNodes []ast.Node
	var groups [][]ast.Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if h, ok := c.(*ast.Heading); ok && h.Level == 3 {
			groups = append(groups, []ast.Node{c})
			continue
		}
		if len(groups) == 0 {
			leadNodes = append(leadNodes, c)
			continue
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], c)
	}
	if lead, err = nr.renderNodes(source, leadNodes...); err != nil {
		return "", "", err
	}
	var b strings.Builder
	for _, group := range groups {
		body, err := nr.renderNodes(source, group...)
		if err != nil {
			return "", "", err
		}
		b.WriteString("<li>")
		b.WriteString(string(body))
		b.WriteString("</li>\n")
	}
	return lead, template.HTML(b.String()), nil
}

func defaultSteps(w io.Writer, _ string, body template.HTML) {
	_, _ = io.WriteString(w, `<ol class="steps">`)
	_, _ = io.WriteString(w, string(body))
	_, _ = io.WriteString(w, "</ol>\n")
}

// calloutFunc renders any other directive as an aside labelled with the
// title, or the capitalised directive name when there is none.
func calloutFunc(kind string) ContainerFunc {
	return func(w io.Writer, title string, body template.HTML) {
		label := title
		if label == "" {
			label = strings.ToUpper(kind[:1]) + kind[1:]
		}
		_, _ = fmt.Fprintf(w, `<aside class="callout" data-kind="%s"><p class="callout-label">%s</p>%s</aside>`+"\n",
			template.HTMLEscapeString(kind), template.HTMLEscapeString(label), body)
	}
}

// renderInlineParagraph drops the <p> wrapper; consecutive paragraphs are
// separated by a newline.
func (nr *nodeRenderer) renderInlineParagraph(w util.BufWriter, _ []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering && node.NextSibling() != nil {
		_ = w.WriteByte('\n')
	}
	return ast.WalkContinue, nil
}

func attrString(v any) string {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case string:
		return t
	default:
		return fmt.Sprint(t)
	}
}
