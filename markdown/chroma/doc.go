// Package chroma adapts the Chroma syntax highlighter to markdown.Highlighter
// so the base markdown package carries no highlighter dependency.
//
//	r := markdown.New(markdown.WithHighlighter(chroma.New()))
//
// By default tokens carry CSS classes; serve Stylesheet output for a named
// Chroma style alongside the page. Its rules are scoped to pre elements, so
// they apply to the markdown package's default code markup without further
// wiring. WithStyle switches to inline styles for output that must stand
// alone.
package chroma
