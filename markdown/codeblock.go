package markdown

import (
	"html/template"
	"io"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// parseInfo splits a fence info string into the language and, when
// withAttrs is set, the key=value attributes that follow it. Attributes may
// be wrapped in braces and values may be bare, single- or double-quoted.
func parseInfo(info string, withAttrs bool) (lang string, attrs map[string]string) {
	info = strings.TrimSpace(info)
	if info == "" {
		return "", nil
	}
	end := strings.IndexFunc(info, unicode.IsSpace)
	if end < 0 {
		return info, nil
	}
	lang = info[:end]
	if !withAttrs {
		return lang, nil
	}
	rest := strings.TrimSpace(info[end:])
	if strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") {
		rest = strings.TrimSpace(rest[1 : len(rest)-1])
	}
	for rest != "" {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			break
		}
		i := strings.IndexAny(rest, "= \t")
		if i < 0 {
			attrs = setAttr(attrs, rest, "")
			break
		}
		key := rest[:i]
		if rest[i] != '=' {
			attrs = setAttr(attrs, key, "")
			rest = rest[i:]
			continue
		}
		rest = rest[i+1:]
		var value string
		if rest != "" && (rest[0] == '"' || rest[0] == '\'') {
			quote := rest[0]
			closing := strings.IndexByte(rest[1:], quote)
			if closing < 0 {
				value, rest = rest[1:], ""
			} else {
				value, rest = rest[1:1+closing], rest[closing+2:]
			}
		} else {
			stop := strings.IndexAny(rest, " \t")
			if stop < 0 {
				value, rest = rest, ""
			} else {
				value, rest = rest[:stop], rest[stop:]
			}
		}
		attrs = setAttr(attrs, key, value)
	}
	return lang, attrs
}

func setAttr(attrs map[string]string, key, value string) map[string]string {
	if key == "" {
		return attrs
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	attrs[key] = value
	return attrs
}

// blockLines joins the source lines of a block node.
func blockLines(n ast.Node, source []byte) string {
	lines := n.Lines()
	var b strings.Builder
	for i := 0; i < lines.Len(); i++ {
		seg := lines.At(i)
		b.Write(seg.Value(source))
	}
	return b.String()
}

// codeBlock assembles the CodeBlock handed to the code block hook.
func (r *Renderer) codeBlock(info, code string) CodeBlock {
	lang, attrs := parseInfo(info, !r.cfg.basic)
	block := CodeBlock{
		Lang:  lang,
		Title: attrs["title"],
		Attrs: attrs,
		Code:  code,
		Body:  template.HTML(util.EscapeHTML([]byte(code))),
	}
	if r.cfg.highlighter != nil && lang != "" {
		if body, ok := r.cfg.highlighter(lang, code); ok {
			block.Body = body
		}
	}
	return block
}

// defaultCodeBlock writes <pre><code class="language-x">, wrapped in a
// figure with a figcaption when the block has a title.
func defaultCodeBlock(w io.Writer, block CodeBlock) {
	var b strings.Builder
	if block.Title != "" {
		b.WriteString(`<figure class="code"><figcaption>`)
		b.WriteString(template.HTMLEscapeString(block.Title))
		b.WriteString(`</figcaption>`)
	}
	b.WriteString("<pre><code")
	if block.Lang != "" {
		b.WriteString(` class="language-`)
		b.WriteString(template.HTMLEscapeString(block.Lang))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	b.WriteString(string(block.Body))
	b.WriteString("</code></pre>")
	if block.Title != "" {
		b.WriteString("</figure>")
	}
	b.WriteByte('\n')
	_, _ = io.WriteString(w, b.String())
}
