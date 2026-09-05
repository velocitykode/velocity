package markdown_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/velocitykode/velocity/markdown"
)

func docsFS() fstest.MapFS {
	return fstest.MapFS{
		"docs/index.md":                   {Data: []byte("---\ntitle: Velship Docs\ndescription: Everything about Velship.\n---\n# Velship Docs\n\nWelcome.\n")},
		"docs/changelog.md":               {Data: []byte("---\ntitle: Changelog\nweight: 9\n---\n# Changelog\n\nRecent changes.\n")},
		"docs/apps/index.md":              {Data: []byte("---\ntitle: Apps\nweight: 2\n---\n# Apps\n\nApps overview.\n")},
		"docs/apps/deploy.md":             {Data: []byte("---\ntitle: Deploy\nweight: 2\ndescription: Ship an app.\n---\n# Deploy\n\nRun `vel deploy`.\n")},
		"docs/apps/create.md":             {Data: []byte("---\ntitle: Create\nweight: 1\n---\n# Create\n\nRun `vel new`.\n")},
		"docs/apps/scale.md":              {Data: []byte("# Scale\n\n" + strings.Repeat("Scale words. ", 40))},
		"docs/getting-started/install.md": {Data: []byte("---\nweight: 1\n---\n# Install\n\nInstall it.\n")},
		"docs/getting-started/_index.md":  {Data: []byte("---\ntitle: Getting Started\nweight: 1\n---\nStart here.\n")},
		"docs/reference/api.md":           {Data: []byte("---\nsection: apps\nweight: 3\n---\n# API\n\nMoved into apps by front matter.\n")},
		"docs/notes.txt":                  {Data: []byte("ignored")},
		"docs/legacy.markdown":            {Data: []byte("# Legacy\n")},
		"other/outside.md":                {Data: []byte("# Outside\n")},
	}
}

func load(t *testing.T) *markdown.Collection {
	t.Helper()
	c, err := markdown.LoadFS(docsFS(), "docs", nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func slugs(pages []*markdown.Page) []string {
	out := make([]string, len(pages))
	for i, p := range pages {
		out[i] = p.Slug
	}
	return out
}

func TestLoadFS_Pages(t *testing.T) {
	c := load(t)
	got := slugs(c.Pages())
	want := []string{
		"", "changelog", "legacy",
		"getting-started", "getting-started/install",
		"apps", "apps/create", "apps/deploy", "reference/api", "apps/scale",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Pages() slugs = %v; want %v", got, want)
	}
	if len(c.Pages()) != 10 {
		t.Errorf("len(Pages()) = %d; want 10 (txt and files outside root skipped)", len(c.Pages()))
	}
}

func TestLoadFS_PageFields(t *testing.T) {
	c := load(t)
	tests := []struct {
		path    string
		slug    string
		section string
		weight  int
		index   bool
		title   string
	}{
		{"index.md", "", "", 0, true, "Velship Docs"},
		{"apps/index.md", "apps", "apps", 2, true, "Apps"},
		{"apps/deploy.md", "apps/deploy", "apps", 2, false, "Deploy"},
		{"apps/scale.md", "apps/scale", "apps", 0, false, "Scale"},
		{"getting-started/_index.md", "getting-started", "getting-started", 1, true, "Getting Started"},
		{"reference/api.md", "reference/api", "apps", 3, false, "API"},
		{"legacy.markdown", "legacy", "", 0, false, "Legacy"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			p, ok := c.Find(tt.path)
			if !ok {
				t.Fatalf("Find(%q) missing", tt.path)
			}
			if p.Slug != tt.slug || p.Section != tt.section || p.Weight != tt.weight || p.Index != tt.index || p.Document.Title != tt.title {
				t.Errorf("page = {Slug:%q Section:%q Weight:%d Index:%v Title:%q}; want {%q %q %d %v %q}",
					p.Slug, p.Section, p.Weight, p.Index, p.Document.Title, tt.slug, tt.section, tt.weight, tt.index, tt.title)
			}
			if strings.Contains(string(p.Source), "---\n") {
				t.Errorf("Source still carries front matter: %q", p.Source)
			}
		})
	}
}

func TestCollection_Tree(t *testing.T) {
	c := load(t)
	tree := c.Tree()
	type sec struct {
		name, title, index string
		pages              []string
	}
	var got []sec
	for _, s := range tree {
		entry := sec{name: s.Name, title: s.Title, pages: slugs(s.Pages)}
		if s.Index != nil {
			entry.index = s.Index.Path
		}
		got = append(got, entry)
	}
	want := []sec{
		{"", "Velship Docs", "index.md", []string{"changelog", "legacy"}},
		{"getting-started", "Getting Started", "getting-started/_index.md", []string{"getting-started/install"}},
		{"apps", "Apps", "apps/index.md", []string{"apps/create", "apps/deploy", "reference/api", "apps/scale"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tree() = %+v; want %+v", got, want)
	}
	// Tree hands out copies: mutating the result does not touch the collection.
	tree[0].Pages[0] = nil
	if c.Tree()[0].Pages[0] == nil {
		t.Error("Tree() shares its Pages slice with the collection")
	}
}

func TestCollection_TreeWithoutIndexPages(t *testing.T) {
	c, err := markdown.LoadFS(fstest.MapFS{
		"b/two.md": {Data: []byte("# Two\n")},
		"a/one.md": {Data: []byte("# One\n")},
		"a/zed.md": {Data: []byte("---\nweight: 5\n---\n# Zed\n")},
	}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := c.Tree()
	if len(tree) != 2 || tree[0].Name != "a" || tree[0].Title != "A" || tree[1].Name != "b" {
		t.Fatalf("Tree() = %+v; want sections a, b by name with humanized titles", tree)
	}
	if got := slugs(tree[0].Pages); !reflect.DeepEqual(got, []string{"a/zed", "a/one"}) {
		t.Errorf("section a pages = %v; want weighted page before the unweighted one", got)
	}
}

func TestCollection_TreeSectionOrder(t *testing.T) {
	c, err := markdown.LoadFS(fstest.MapFS{
		"zeta/index.md":  {Data: []byte("---\nweight: 1\n---\n# Zeta\n")},
		"alpha/index.md": {Data: []byte("# Alpha without weight\n")},
		"mid/index.md":   {Data: []byte("---\nweight: 2\n---\n# Mid\n")},
		"noindex/x.md":   {Data: []byte("# X\n")},
		"root.md":        {Data: []byte("# Root page\n")},
	}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range c.Tree() {
		got = append(got, s.Name)
	}
	// Root first; weighted sections by weight; unweighted (no weight, or no
	// index page) after them by name.
	want := []string{"", "zeta", "mid", "alpha", "noindex"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("section order = %v; want %v", got, want)
	}
}

func TestCollection_Find(t *testing.T) {
	c := load(t)
	tests := []struct {
		key  string
		want string
		ok   bool
	}{
		{"apps/deploy.md", "apps/deploy.md", true},
		{"apps/deploy", "apps/deploy.md", true},
		{"/apps/deploy/", "apps/deploy.md", true},
		{"", "index.md", true},
		{"/", "index.md", true},
		{"apps", "apps/index.md", true},
		{"missing", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			p, ok := c.Find(tt.key)
			if ok != tt.ok {
				t.Fatalf("Find(%q) ok = %v; want %v", tt.key, ok, tt.ok)
			}
			if ok && p.Path != tt.want {
				t.Errorf("Find(%q) = %q; want %q", tt.key, p.Path, tt.want)
			}
		})
	}
}

func TestCollection_PrevNext(t *testing.T) {
	c := load(t)
	page := func(path string) *markdown.Page {
		p, ok := c.Find(path)
		if !ok {
			t.Fatalf("missing %s", path)
		}
		return p
	}
	slugOf := func(p *markdown.Page) string {
		if p == nil {
			return "<nil>"
		}
		return p.Slug
	}
	tests := []struct {
		path       string
		prev, next string
	}{
		{"apps/index.md", "<nil>", "apps/create"},
		{"apps/create.md", "apps", "apps/deploy"},
		{"apps/scale.md", "reference/api", "<nil>"},
		{"index.md", "<nil>", "changelog"},
		{"legacy.markdown", "changelog", "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			p := page(tt.path)
			if got := slugOf(c.Prev(p)); got != tt.prev {
				t.Errorf("Prev = %q; want %q", got, tt.prev)
			}
			if got := slugOf(c.Next(p)); got != tt.next {
				t.Errorf("Next = %q; want %q", got, tt.next)
			}
		})
	}
	if c.Prev(&markdown.Page{}) != nil || c.Next(&markdown.Page{}) != nil {
		t.Error("Prev/Next of a foreign page must be nil")
	}
}

func TestPage_URL(t *testing.T) {
	tests := []struct {
		slug, base, want string
	}{
		{"apps/deploy", "/docs", "/docs/apps/deploy"},
		{"apps/deploy", "/docs/", "/docs/apps/deploy"},
		{"apps/deploy", "https://velship.com/docs", "https://velship.com/docs/apps/deploy"},
		{"apps/deploy", "", "/apps/deploy"},
		{"", "/docs", "/docs"},
		{"", "", "/"},
	}
	for _, tt := range tests {
		p := &markdown.Page{Slug: tt.slug}
		if got := p.URL(tt.base); got != tt.want {
			t.Errorf("URL(%q) for slug %q = %q; want %q", tt.base, tt.slug, got, tt.want)
		}
	}
}

func TestCollection_SearchIndex(t *testing.T) {
	c := load(t)
	entries := c.SearchIndex("/docs")
	byURL := map[string]markdown.SearchEntry{}
	for _, e := range entries {
		byURL[e.URL] = e
	}
	if len(entries) != 10 {
		t.Fatalf("len = %d; want 10", len(entries))
	}
	tests := []struct {
		url     string
		title   string
		section string
		excerpt string
	}{
		{"/docs", "Velship Docs", "", "Everything about Velship."},
		{"/docs/apps/deploy", "Deploy", "apps", "Ship an app."},
		{"/docs/apps/create", "Create", "apps", "Run vel new."},
		{"/docs/reference/api", "API", "apps", "Moved into apps by front matter."},
	}
	for _, tt := range tests {
		e, ok := byURL[tt.url]
		if !ok {
			t.Errorf("missing entry for %s", tt.url)
			continue
		}
		if e.Title != tt.title || e.Section != tt.section || e.Excerpt != tt.excerpt {
			t.Errorf("entry %s = %+v; want title %q section %q excerpt %q", tt.url, e, tt.title, tt.section, tt.excerpt)
		}
	}
	scale := byURL["/docs/apps/scale"].Excerpt
	if len([]rune(scale)) > 200 || !strings.HasPrefix(scale, "Scale words.") || strings.HasSuffix(scale, " ") || strings.HasSuffix(scale, "wor") {
		t.Errorf("long excerpt not cut on a word boundary within 200 runes: %q", scale)
	}
}

func TestCollection_LLMSText(t *testing.T) {
	c := load(t)
	got := c.LLMSText("https://velship.com/docs")
	want := `# Velship Docs

> Everything about Velship.

## Pages

- [Changelog](https://velship.com/docs/changelog): Recent changes.
- [Legacy](https://velship.com/docs/legacy)

## Getting Started

- [Getting Started](https://velship.com/docs/getting-started): Start here.
- [Install](https://velship.com/docs/getting-started/install): Install it.

## Apps

- [Apps](https://velship.com/docs/apps): Apps overview.
- [Create](https://velship.com/docs/apps/create): Run vel new.
- [Deploy](https://velship.com/docs/apps/deploy): Ship an app.
- [API](https://velship.com/docs/reference/api): Moved into apps by front matter.
- [Scale](https://velship.com/docs/apps/scale): ` + strings.TrimSpace(strings.Repeat("Scale words. ", 15)) + "\n"
	if got != want {
		t.Errorf("LLMSText mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestCollection_LLMSText_NoRootIndex(t *testing.T) {
	c, err := markdown.LoadFS(fstest.MapFS{"a/x.md": {Data: []byte("# X\n\nBody.\n")}}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Documentation\n\n## A\n\n- [X](/a/x): Body.\n"
	if got := c.LLMSText(""); got != want {
		t.Errorf("LLMSText = %q; want %q", got, want)
	}
}

func TestCollection_LLMSFullText(t *testing.T) {
	c, err := markdown.LoadFS(fstest.MapFS{
		"index.md":    {Data: []byte("---\ntitle: Site\n---\n# Site\n\nIntro.\n")},
		"a/one.md":    {Data: []byte("---\ntitle: One\n---\n# Different heading\n\n```go\nx := 1\n```\n")},
		"a/two.md":    {Data: []byte("Only body.\n")},
		"a/_index.md": {Data: []byte("# A\n")},
	}, ".", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := c.LLMSFullText("/docs")
	want := `# Site

---

# Site

URL: /docs

Intro.

---

# A

URL: /docs/a

---

# One

URL: /docs/a/one

# Different heading

` + "```go\nx := 1\n```" + `

---

# Two

URL: /docs/a/two

Only body.
`
	if got != want {
		t.Errorf("LLMSFullText mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestLoadFS_Errors(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		root string
		want string
	}{
		{"nil fs", nil, ".", "nil file system"},
		{"missing root", fstest.MapFS{"a.md": {Data: []byte("x")}}, "docs", "load docs"},
		{"bad front matter names the file", fstest.MapFS{"docs/bad.md": {Data: []byte("---\ntitle: [\n---\n")}}, "docs", "docs/bad.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fsys fstest.MapFS
			if tt.fsys != nil {
				fsys = tt.fsys
			}
			var err error
			if tt.fsys == nil {
				_, err = markdown.LoadFS(nil, tt.root, nil)
			} else {
				_, err = markdown.LoadFS(fsys, tt.root, nil)
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v; want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoadFS_UsesGivenRenderer(t *testing.T) {
	r := markdown.New(markdown.Basic())
	c, err := markdown.LoadFS(fstest.MapFS{"x.md": {Data: []byte("## H\n")}}, ".", r)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(c.Pages()[0].Document.HTML); got != "<h2>H</h2>\n" {
		t.Errorf("HTML = %q; want Basic() markup", got)
	}
	c, err = markdown.LoadFS(fstest.MapFS{"x.md": {Data: []byte("## H\n")}}, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pages()) != 1 || c.Pages()[0].Path != "x.md" {
		t.Errorf("root \"/\" should behave like \".\": %+v", c.Pages())
	}
}
