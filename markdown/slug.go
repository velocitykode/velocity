package markdown

import (
	"strconv"
	"strings"
	"unicode"
)

// Slug converts heading text to a GitHub-style anchor id: lower-cased,
// punctuation removed, spaces replaced by hyphens, letters and digits of any
// script kept. An empty result becomes "section".
func Slug(text string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(text)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r), r == '_', r == '-':
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		return "section"
	}
	return b.String()
}

// slugger hands out unique ids within one document: a repeated slug gets a
// -1, -2, ... suffix, matching GitHub.
type slugger struct {
	seen map[string]bool
}

func newSlugger() *slugger {
	return &slugger{seen: map[string]bool{}}
}

// reserve records an explicit id so generated ids never collide with it.
func (s *slugger) reserve(id string) {
	s.seen[id] = true
}

func (s *slugger) unique(text string) string {
	base := Slug(text)
	if !s.seen[base] {
		s.seen[base] = true
		return base
	}
	for i := 1; ; i++ {
		candidate := base + "-" + strconv.Itoa(i)
		if !s.seen[candidate] {
			s.seen[candidate] = true
			return candidate
		}
	}
}
