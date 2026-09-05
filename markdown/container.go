package markdown

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// containerNode is a fenced container directive:
//
//	:::name optional title
//	body
//	:::
type containerNode struct {
	ast.BaseBlock
	name  string
	title string
	fence int
}

var kindContainer = ast.NewNodeKind("VelocityContainer")

func (n *containerNode) Kind() ast.NodeKind { return kindContainer }

func (n *containerNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Name": n.name, "Title": n.title}, nil)
}

type containerParser struct{}

func (containerParser) Trigger() []byte { return []byte{':'} }

func (containerParser) Open(_ ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, _ := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos >= len(line) || line[pos] != ':' {
		return nil, parser.NoChildren
	}
	i := pos
	for i < len(line) && line[i] == ':' {
		i++
	}
	fence := i - pos
	if fence < 3 {
		return nil, parser.NoChildren
	}
	name, title := splitDirective(string(line[i:]))
	if !validContainerName(name) {
		return nil, parser.NoChildren
	}
	reader.AdvanceToEOL()
	return &containerNode{name: name, title: title, fence: fence}, parser.HasChildren
}

func (containerParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	n := node.(*containerNode)
	if blocks := pc.OpenedBlocks(); len(blocks) > 0 {
		// The innermost open block is a descendant of this container; while
		// it is a code fence, a line of colons is code, not the closing fence.
		if _, inFence := blocks[len(blocks)-1].Node.(*ast.FencedCodeBlock); inFence {
			return parser.Continue | parser.HasChildren
		}
	}
	line, _ := reader.PeekLine()
	w, pos := util.IndentWidth(line, reader.LineOffset())
	if w < 4 && pos < len(line) && line[pos] == ':' {
		i := pos
		for i < len(line) && line[i] == ':' {
			i++
		}
		if i-pos >= n.fence && util.IsBlank(line[i:]) {
			reader.AdvanceToEOL()
			return parser.Close
		}
	}
	return parser.Continue | parser.HasChildren
}

func (containerParser) Close(ast.Node, text.Reader, parser.Context) {}

func (containerParser) CanInterruptParagraph() bool { return true }

func (containerParser) CanAcceptIndentedLine() bool { return false }

// splitDirective splits "name optional title" into a lower-cased name and a
// trimmed title.
func splitDirective(rest string) (name, title string) {
	rest = strings.TrimSpace(rest)
	i := strings.IndexAny(rest, " \t")
	if i < 0 {
		return strings.ToLower(rest), ""
	}
	return strings.ToLower(rest[:i]), strings.TrimSpace(rest[i:])
}

// validContainerName accepts [A-Za-z][A-Za-z0-9_-]*.
func validContainerName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		alpha := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		digit := r >= '0' && r <= '9'
		switch {
		case alpha:
		case i > 0 && (digit || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return true
}

type containerExtension struct{}

func (containerExtension) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(parser.WithBlockParsers(util.Prioritized(containerParser{}, 50)))
}
