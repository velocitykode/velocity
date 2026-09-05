package markdown

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// excerptRunes bounds the plain-text excerpt of a search entry or llms.txt
// line when the page has no front matter description.
const excerptRunes = 200

// Page is one rendered Markdown file of a Collection.
type Page struct {
	// Path is the file path relative to the collection root, for example
	// "apps/deploy.md".
	Path string
	// Slug is Path without its extension. Index pages (index.md, _index.md,
	// README.md) take their directory, so "apps/index.md" is "apps" and the
	// root index is "".
	Slug string
	// Section is the front matter "section" when set, otherwise the first
	// directory of Path, "" for root-level files.
	Section string
	// Weight is the front matter "weight", 0 when absent. Lower sorts first;
	// unweighted pages follow every weighted one.
	Weight int
	// Index reports whether the file is an index page.
	Index bool
	// Document is the rendered page.
	Document *Document
	// Source is the Markdown body with the front matter removed.
	Source []byte
}

// URL joins baseURL and the page slug: "/docs" and "apps/deploy" give
// "/docs/apps/deploy"; the root index gives baseURL itself, or "/" when
// baseURL is empty.
func (p *Page) URL(baseURL string) string {
	base := strings.TrimRight(baseURL, "/")
	if p.Slug == "" {
		if base == "" {
			return "/"
		}
		return base
	}
	return base + "/" + p.Slug
}

// Section groups the pages that share a section name.
type Section struct {
	// Name is the section name; "" for root-level pages.
	Name string
	// Title is the index page title, or Name with separators replaced by
	// spaces and words capitalised when there is no index page.
	Title string
	// Index is the section's index page, nil when it has none.
	Index *Page
	// Pages holds the other pages ordered by weight (unweighted last), then
	// title, then path.
	Pages []*Page
}

// SearchEntry is one row of a search index.
type SearchEntry struct {
	Title   string
	Section string
	URL     string
	Excerpt string
}

// Collection is a set of rendered pages with the navigation a site needs.
// Build one with LoadFS; it is immutable afterwards and safe for concurrent
// use.
type Collection struct {
	pages    []*Page
	sections []Section
	byPath   map[string]*Page
	bySlug   map[string]*Page
	position map[*Page]pagePosition
}

type pagePosition struct {
	section int
	index   int
}

// LoadFS renders every .md and .markdown file below root in fsys with r (a
// default Renderer when nil) and returns the pages as a Collection. root is
// "." for the whole file system. The first file that fails to render aborts
// the load with an error naming the file.
func LoadFS(fsys fs.FS, root string, r *Renderer) (*Collection, error) {
	if fsys == nil {
		return nil, errors.New("velocity/markdown: load: nil file system")
	}
	if r == nil {
		r = New()
	}
	root = path.Clean(root)
	if root == "/" {
		root = "."
	}
	var pages []*Page
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := path.Ext(p); ext != ".md" && ext != ".markdown" {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		doc, body, err := r.render(data)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		rel := p
		if root != "." {
			rel = strings.TrimPrefix(p, root+"/")
		}
		pages = append(pages, newPage(rel, doc, body))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("velocity/markdown: load %s: %w", root, err)
	}
	return newCollection(pages), nil
}

func newPage(rel string, doc *Document, body []byte) *Page {
	stem := strings.TrimSuffix(rel, path.Ext(rel))
	dir, base := path.Split(stem)
	dir = strings.TrimSuffix(dir, "/")
	index := base == "index" || base == "_index" || base == "README"
	slug := stem
	if index {
		slug = dir
	}
	section := ""
	if i := strings.Index(rel, "/"); i >= 0 {
		section = rel[:i]
	}
	if s, ok := doc.FrontMatter["section"].(string); ok && s != "" {
		section = s
	}
	return &Page{
		Path:     rel,
		Slug:     slug,
		Section:  section,
		Weight:   weightOf(doc.FrontMatter),
		Index:    index,
		Document: doc,
		Source:   body,
	}
}

func weightOf(meta map[string]any) int {
	switch v := meta["weight"].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return 0
}

func newCollection(pages []*Page) *Collection {
	groups := map[string][]*Page{}
	var names []string
	for _, p := range pages {
		if _, ok := groups[p.Section]; !ok {
			names = append(names, p.Section)
		}
		groups[p.Section] = append(groups[p.Section], p)
	}

	sections := make([]Section, 0, len(names))
	for _, name := range names {
		sec := Section{Name: name}
		for _, p := range groups[name] {
			if p.Index && p.Slug == name && sec.Index == nil {
				sec.Index = p
				continue
			}
			sec.Pages = append(sec.Pages, p)
		}
		sort.SliceStable(sec.Pages, func(i, j int) bool {
			a, b := sec.Pages[i], sec.Pages[j]
			if wa, wb := sortWeight(a.Weight), sortWeight(b.Weight); wa != wb {
				return wa < wb
			}
			if ta, tb := pageTitle(a), pageTitle(b); ta != tb {
				return ta < tb
			}
			return a.Path < b.Path
		})
		sec.Title = humanize(name)
		if sec.Index != nil && sec.Index.Document.Title != "" {
			sec.Title = sec.Index.Document.Title
		}
		sections = append(sections, sec)
	}
	sort.SliceStable(sections, func(i, j int) bool {
		a, b := sections[i], sections[j]
		if (a.Name == "") != (b.Name == "") {
			return a.Name == ""
		}
		if wa, wb := sectionWeight(a), sectionWeight(b); wa != wb {
			return wa < wb
		}
		return a.Name < b.Name
	})

	c := &Collection{
		sections: sections,
		byPath:   map[string]*Page{},
		bySlug:   map[string]*Page{},
		position: map[*Page]pagePosition{},
	}
	for si, sec := range sections {
		for pi, p := range sectionPages(sec) {
			c.pages = append(c.pages, p)
			c.byPath[p.Path] = p
			c.bySlug[p.Slug] = p
			c.position[p] = pagePosition{section: si, index: pi}
		}
	}
	return c
}

func sectionWeight(s Section) int {
	if s.Index != nil {
		return sortWeight(s.Index.Weight)
	}
	return sortWeight(0)
}

// sortWeight orders explicit weights ascending and places the unweighted
// (zero) after every weighted entry, so authors weight what matters and the
// rest follow alphabetically.
func sortWeight(w int) int {
	if w == 0 {
		return math.MaxInt
	}
	return w
}

// sectionPages returns the section's pages in navigation order: the index
// page first, then Pages.
func sectionPages(s Section) []*Page {
	out := make([]*Page, 0, len(s.Pages)+1)
	if s.Index != nil {
		out = append(out, s.Index)
	}
	return append(out, s.Pages...)
}

func pageTitle(p *Page) string {
	if p.Document.Title != "" {
		return p.Document.Title
	}
	if p.Slug == "" {
		return "Home"
	}
	return humanize(path.Base(p.Slug))
}

// humanize turns "getting-started" into "Getting Started".
func humanize(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	for i, w := range words {
		r, size := utf8.DecodeRuneInString(w)
		words[i] = string(unicode.ToUpper(r)) + w[size:]
	}
	return strings.Join(words, " ")
}

// Pages returns every page in navigation order: sections in tree order, each
// section's index page first, then its pages by weight.
func (c *Collection) Pages() []*Page {
	out := make([]*Page, len(c.pages))
	copy(out, c.pages)
	return out
}

// Tree returns the sections in order: root-level pages first, then sections
// by their index page weight (sections without a weighted index last), then
// by name.
func (c *Collection) Tree() []Section {
	out := make([]Section, len(c.sections))
	for i, s := range c.sections {
		out[i] = s
		out[i].Pages = make([]*Page, len(s.Pages))
		copy(out[i].Pages, s.Pages)
	}
	return out
}

// Find returns the page whose Path or Slug equals key, ignoring a leading
// slash and a trailing slash on the slug.
func (c *Collection) Find(key string) (*Page, bool) {
	key = strings.TrimPrefix(key, "/")
	if p, ok := c.byPath[key]; ok {
		return p, true
	}
	p, ok := c.bySlug[strings.TrimSuffix(key, "/")]
	return p, ok
}

// Prev returns the page before p within its section, or nil at the start of
// the section or when p is not part of the collection.
func (c *Collection) Prev(p *Page) *Page {
	return c.neighbour(p, -1)
}

// Next returns the page after p within its section, or nil at the end of the
// section or when p is not part of the collection.
func (c *Collection) Next(p *Page) *Page {
	return c.neighbour(p, 1)
}

func (c *Collection) neighbour(p *Page, step int) *Page {
	pos, ok := c.position[p]
	if !ok {
		return nil
	}
	pages := sectionPages(c.sections[pos.section])
	i := pos.index + step
	if i < 0 || i >= len(pages) {
		return nil
	}
	return pages[i]
}

// SearchIndex returns one entry per page in navigation order. Excerpt is the
// front matter description when present, otherwise the opening words of the
// plain text.
func (c *Collection) SearchIndex(baseURL string) []SearchEntry {
	out := make([]SearchEntry, 0, len(c.pages))
	for _, p := range c.pages {
		out = append(out, SearchEntry{
			Title:   pageTitle(p),
			Section: p.Section,
			URL:     p.URL(baseURL),
			Excerpt: excerpt(p),
		})
	}
	return out
}

func excerpt(p *Page) string {
	if d, ok := p.Document.FrontMatter["description"].(string); ok && strings.TrimSpace(d) != "" {
		return strings.TrimSpace(d)
	}
	plain := p.Document.Plain
	if first, rest, ok := strings.Cut(plain, "\n"); ok && first == p.Document.Title {
		plain = rest
	} else if !ok && plain == p.Document.Title {
		plain = ""
	}
	return truncateWords(strings.Join(strings.Fields(plain), " "), excerptRunes)
}

// truncateWords cuts s to at most n runes on a word boundary.
func truncateWords(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := string([]rune(s)[:n])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return cut
}

// LLMSText renders an llms.txt index: an H1 with the root index title (or
// "Documentation"), its description as a blockquote when present, then one
// H2 per section listing "- [title](url): excerpt" per page. Root-level pages
// appear under "Pages".
func (c *Collection) LLMSText(baseURL string) string {
	var b strings.Builder
	root := c.rootIndex()
	b.WriteString("# ")
	b.WriteString(c.siteTitle())
	b.WriteString("\n")
	if root != nil {
		if d, ok := root.Document.FrontMatter["description"].(string); ok && strings.TrimSpace(d) != "" {
			b.WriteString("\n> ")
			b.WriteString(strings.TrimSpace(d))
			b.WriteString("\n")
		}
	}
	for _, sec := range c.sections {
		pages := sec.Pages
		title := sec.Title
		if sec.Name == "" {
			title = "Pages"
		} else if sec.Index != nil {
			pages = sectionPages(sec)
		}
		if len(pages) == 0 {
			continue
		}
		b.WriteString("\n## ")
		b.WriteString(title)
		b.WriteString("\n\n")
		for _, p := range pages {
			b.WriteString("- [")
			b.WriteString(pageTitle(p))
			b.WriteString("](")
			b.WriteString(p.URL(baseURL))
			b.WriteString(")")
			if e := excerpt(p); e != "" {
				b.WriteString(": ")
				b.WriteString(e)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// LLMSFullText renders an llms-full.txt: the site title, then every page in
// navigation order as an H1 with its title, a "URL:" line and the Markdown
// body (a leading h1 repeating the title is dropped), separated by thematic
// breaks.
func (c *Collection) LLMSFullText(baseURL string) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(c.siteTitle())
	b.WriteString("\n")
	for _, p := range c.pages {
		title := pageTitle(p)
		b.WriteString("\n---\n\n# ")
		b.WriteString(title)
		b.WriteString("\n\nURL: ")
		b.WriteString(p.URL(baseURL))
		b.WriteString("\n")
		body := strings.TrimSpace(string(p.Source))
		if first, rest, _ := strings.Cut(body, "\n"); strings.HasPrefix(first, "# ") && strings.TrimSpace(first[2:]) == title {
			body = strings.TrimSpace(rest)
		}
		if body != "" {
			b.WriteString("\n")
			b.WriteString(body)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (c *Collection) rootIndex() *Page {
	if p, ok := c.bySlug[""]; ok && p.Index {
		return p
	}
	return nil
}

func (c *Collection) siteTitle() string {
	if root := c.rootIndex(); root != nil && root.Document.Title != "" {
		return root.Document.Title
	}
	return "Documentation"
}
