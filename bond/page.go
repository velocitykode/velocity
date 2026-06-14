package bond

import (
	"encoding/json"
	"html"
	"strings"
)

// OnceMeta tracks a once-prop for the client so it knows which props
// have already been delivered and should not be sent again.
type OnceMeta struct {
	Prop string `json:"prop"`
}

// Page represents an Inertia page response
type Page struct {
	Component string `json:"component"`
	Props     Props  `json:"props"`
	// Flash carries one-shot flash data per the Inertia v2 protocol. The
	// client exposes it via usePage().flash and does NOT persist it in
	// history. Omitted from the payload when empty.
	Flash          map[string]any        `json:"flash,omitempty"`
	URL            string                `json:"url"`
	Version        string                `json:"version"`
	EncryptHistory bool                  `json:"encryptHistory,omitempty"`
	ClearHistory   bool                  `json:"clearHistory,omitempty"`
	DeferredProps  map[string][]string   `json:"deferredProps,omitempty"`
	MergeProps     []string              `json:"mergeProps,omitempty"`
	PrependProps   []string              `json:"prependProps,omitempty"`
	DeepMergeProps []string              `json:"deepMergeProps,omitempty"`
	MatchPropsOn   map[string][]string   `json:"matchPropsOn,omitempty"`
	ScrollProps    map[string]ScrollMeta `json:"scrollProps,omitempty"`
	OnceProps      map[string]OnceMeta   `json:"onceProps,omitempty"`
}

// ToJSON returns the JSON representation of the page
func (p Page) ToJSON() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ToHTMLAttr returns JSON escaped for use in HTML data attributes
// Single quotes in the JSON are escaped since we use data-page='...'
func (p Page) ToHTMLAttr() (string, error) {
	jsonStr, err := p.ToJSON()
	if err != nil {
		return "", err
	}
	return htmlAttrEscape(jsonStr), nil
}

// htmlAttrEscape escapes already-marshaled page JSON for safe embedding in a
// single-quoted data-page='...' attribute. Callers that have the JSON bytes in
// hand use this directly to avoid re-marshaling the Page.
//
// html.EscapeString handles <, >, &, " but NOT single quotes. Since we embed in
// a single-quoted attribute, also escape ' to prevent attribute breakout (e.g.
// prop values like "O'Brien").
func htmlAttrEscape(jsonStr string) string {
	escaped := html.EscapeString(jsonStr)
	return strings.ReplaceAll(escaped, "'", "&#39;")
}
