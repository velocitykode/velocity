package markdown_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

// FuzzRender pins the safety contract of the default Renderer on arbitrary
// bytes: it never panics, never errors on input without front matter, and
// the output carries no raw script element and no javascript: destination.
func FuzzRender(f *testing.F) {
	seeds := []string{
		"<script>alert(1)</script>",
		"[x](javascript:alert(1))",
		"[x](JAVASCRIPT&colon;alert(1))",
		"![x](data:text/html,<script>)",
		"<javascript:alert(1)>",
		"::::steps\n### a\n:::note\nb\n:::\n::::",
		":::note\nunclosed",
		"```go title=\"x\"\n<script>\n```",
		"# A\n## A\n## A {#a}\n### A",
		"---\ntitle: [\n---\n",
		"a\r\nb\r\n\r\n- c\r\n",
		"| a |\n|---|\n| <b> |",
		"\xff\xfe\x00junk\u200b ",
		"[^1]: note\n\nText[^1] :::note :::",
		strings.Repeat(":", 500) + "note\n" + strings.Repeat(":", 500),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	r := markdown.New()
	f.Fuzz(func(t *testing.T, src []byte) {
		doc, err := r.Render(src)
		if err != nil {
			// Only malformed front matter errors, and only when the source
			// opens with a fence.
			if !bytes.HasPrefix(bytes.TrimPrefix(src, []byte("\xEF\xBB\xBF")), []byte("---")) {
				t.Fatalf("Render(%q) errored without a front matter fence: %v", src, err)
			}
			return
		}
		html := strings.ToLower(string(doc.HTML))
		if strings.Contains(html, "<script") {
			t.Fatalf("raw script survived: %q -> %q", src, doc.HTML)
		}
		for _, attr := range []string{`href="javascript:`, `src="javascript:`, `href="vbscript:`, `href="file:`, `href="data:text`} {
			if strings.Contains(html, attr) {
				t.Fatalf("unsafe destination survived: %q -> %q", src, doc.HTML)
			}
		}
	})
}

// FuzzParseFrontMatter pins that the splitter never panics and that a
// successful split returns a body that is a suffix of the input.
func FuzzParseFrontMatter(f *testing.F) {
	for _, s := range []string{"---\ntitle: x\n---\nbody", "---\n---", "---", "\xEF\xBB\xBF---\na: 1\n...\n", "---\n- a\n---\n", "no fm"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		meta, body, err := markdown.ParseFrontMatter(src)
		if err != nil {
			if meta != nil || body != nil {
				t.Fatalf("error result must be nil, nil: %v %v", meta, body)
			}
			return
		}
		if meta == nil {
			t.Fatal("meta must never be nil on success")
		}
		if !bytes.HasSuffix(bytes.TrimPrefix(src, []byte("\xEF\xBB\xBF")), body) {
			t.Fatalf("body %q is not a suffix of %q", body, src)
		}
	})
}
