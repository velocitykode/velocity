package markdown_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/markdown"
)

var update = flag.Bool("update", false, "rewrite the golden HTML files under testdata/golden")

// TestRender_Golden pins the exact markup for every directive, code block
// shape and safety default. Each testdata/golden/<name>.md renders with the
// default Renderer (or the options named in goldenOptions) and must equal
// <name>.html byte for byte. Run with -update to regenerate after an
// intentional change, then review the diff.
func TestRender_Golden(t *testing.T) {
	sources, err := filepath.Glob(filepath.Join("testdata", "golden", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) == 0 {
		t.Fatal("no golden sources found")
	}
	for _, src := range sources {
		name := strings.TrimSuffix(filepath.Base(src), ".md")
		t.Run(name, func(t *testing.T) {
			input, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}
			r := markdown.New(goldenOptions[name]...)
			doc, err := r.Render(input)
			if err != nil {
				t.Fatalf("Render(%s): %v", name, err)
			}
			goldenPath := strings.TrimSuffix(src, ".md") + ".html"
			if *update {
				if err := os.WriteFile(goldenPath, []byte(doc.HTML), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden file %s (run with -update): %v", goldenPath, err)
			}
			if string(doc.HTML) != string(want) {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, doc.HTML, want)
			}
		})
	}
}

// goldenOptions names the options a golden case renders with; every other
// case uses the defaults.
var goldenOptions = map[string][]markdown.Option{
	"safety-allow-html": {markdown.AllowHTML()},
	"basic":             {markdown.Basic()},
	"inline":            {markdown.Inline()},
	"link-rewrite": {markdown.WithLinkRewrite(func(href string) string {
		// Map relative Markdown paths to site routes; leave everything else.
		if strings.Contains(href, "://") || !strings.HasSuffix(href, ".md") {
			return href
		}
		href = strings.TrimPrefix(strings.TrimPrefix(href, "../"), "./")
		return "/docs/" + strings.TrimSuffix(href, ".md")
	})},
}
