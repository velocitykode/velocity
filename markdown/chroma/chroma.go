package chroma

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"

	"github.com/velocitykode/velocity/markdown"
)

// Option configures New.
type Option func(*config)

type config struct {
	style string
}

// WithStyle emits inline style attributes from the named Chroma style (for
// example "github" or "monokai") instead of CSS classes. Only the tokens
// carry styles; give the surrounding pre the style's background yourself. An
// unknown name falls back to Chroma's default style.
func WithStyle(name string) Option {
	return func(c *config) { c.style = name }
}

// New returns a markdown.Highlighter backed by Chroma. It reports false, so
// the escaped source is used, when the language is unknown to Chroma.
func New(opts ...Option) markdown.Highlighter {
	var cfg config
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	formatterOpts := []html.Option{html.PreventSurroundingPre(true)}
	style := styles.Fallback
	if cfg.style == "" {
		formatterOpts = append(formatterOpts, html.WithClasses(true))
	} else {
		style = styles.Get(cfg.style)
	}
	formatter := html.New(formatterOpts...)

	return func(lang, code string) (template.HTML, bool) {
		lexer := lexers.Get(lang)
		if lexer == nil {
			return "", false
		}
		iterator, err := chroma.Coalesce(lexer).Tokenise(nil, code)
		if err != nil {
			return "", false
		}
		var buf bytes.Buffer
		if err := formatter.Format(&buf, style, iterator); err != nil {
			return "", false
		}
		return template.HTML(buf.String()), true
	}
}

// Stylesheet returns the CSS that colours class-based output for the named
// Chroma style. Rules are scoped to pre elements, matching the markup the
// markdown package emits (<pre><code class="language-go"> around the
// highlighted spans), so the sheet applies to the generated HTML as is: the
// pre takes the style's background and text colour and every token class
// gets its colour. Custom markdown.WithCodeBlock markup keeps the tokens
// inside a pre for the same effect. It returns an error for an unknown
// style name.
func Stylesheet(name string) (string, error) {
	style, ok := styles.Registry[strings.ToLower(name)]
	if !ok {
		return "", fmt.Errorf("velocity/markdown/chroma: unknown style %q", name)
	}
	var buf bytes.Buffer
	if err := html.New(html.WithClasses(true)).WriteCSS(&buf, style); err != nil {
		return "", fmt.Errorf("velocity/markdown/chroma: stylesheet: %w", err)
	}
	// Chroma writes one rule per line as "/* Token */ .chroma .cls { ... }"
	// with the pre wrapper itself as "/* PreWrapper */ .chroma { ... }" and a
	// duplicate background rule on ".bg". Re-scope the wrapper class to pre
	// and drop the duplicate.
	var out strings.Builder
	for _, line := range strings.Split(buf.String(), "\n") {
		if line == "" || strings.Contains(line, "*/ .bg ") {
			continue
		}
		rescoped := strings.Replace(line, "*/ .chroma", "*/ pre", 1)
		if strings.Contains(rescoped, ".chroma") {
			return "", fmt.Errorf("velocity/markdown/chroma: stylesheet: unexpected rule %q", line)
		}
		out.WriteString(rescoped)
		out.WriteByte('\n')
	}
	return out.String(), nil
}
